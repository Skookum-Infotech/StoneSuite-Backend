package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

func resolveCustomer(ctx context.Context, pool *pgxpool.Pool, customerUUID string) (int, error) {
	var id int
	err := pool.QueryRow(ctx,
		`SELECT customer_id FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: "Unknown or deleted customer."}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve customer: %w", err)
	}
	return id, nil
}

func resolveMethod(ctx context.Context, pool *pgxpool.Pool, methodID int) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT TRUE FROM lkp_payment_method WHERE payment_method_id = $1 AND payment_method_deleted_at IS NULL AND payment_method_is_active`,
		methodID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "Unknown or inactive payment method."}
	}
	if err != nil {
		return fmt.Errorf("resolve payment method: %w", err)
	}
	return nil
}

// Create inserts a new payment header inside one transaction (resolves the
// customer + method, validates custom fields, inserts the row, assigns the
// payment number, writes the 'create' history row) and applies each inline
// application in that same transaction via applyTx, so a failing application
// leaves no payment behind and a retry cannot create a second one. A
// non-empty IdempotencyKey makes a retried create return the first payment.
// New payments start at PEND.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreatePaymentInput, actorEmployeeID int) (*Payment, error) {
	if in.CustomerUUID == "" {
		return nil, ClientError{Msg: "customerUuid is required."}
	}
	if err := validateAmount("amount", in.Amount); err != nil {
		return nil, err
	}
	for _, app := range in.Applications {
		if err := validateAmount("application amount", app.Amount); err != nil {
			return nil, err
		}
	}
	// A retry whose first attempt already committed gets that payment back
	// before any database-dependent check: a replay must answer like the original.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}
	msg, err := workflow.CustomerNotUsableByUUID(ctx, pool, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	if msg != "" {
		return nil, ClientError{Msg: msg}
	}

	custID, err := resolveCustomer(ctx, pool, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	if err := resolveMethod(ctx, pool, in.MethodID); err != nil {
		return nil, err
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	typeID, err := typeIDByCode(ctx, pool, "PYMT")
	if err != nil {
		return nil, err
	}
	pendStatusID, err := statusIDByCode(ctx, pool, typeID, "PEND")
	if err != nil {
		return nil, err
	}

	// PEND is both payment's creation default and its approval checkpoint
	// (unlike modules where the checkpoint is entered later via a manual
	// Transition) -- a payment with zero approvers configured there starts
	// life already Approved instead of parking on a status literally
	// labeled "Pending" that nothing is actually pending on. Mirrors
	// Transition's own auto-skip (store_transition.go).
	initialStatusID := pendStatusID
	initialApprovalStatus := approvalchain.StatusNone
	if gate, gated := moduleConfig().GateFor("PEND"); gated {
		required, err := approvalchain.ActiveApproverCount(ctx, pool, moduleConfig().ApproverTable, typeID, pendStatusID)
		if err != nil {
			return nil, err
		}
		if required == 0 {
			initialStatusID, err = statusIDByCode(ctx, pool, typeID, gate.TargetStatusCode)
			if err != nil {
				return nil, err
			}
			initialApprovalStatus = approvalchain.StatusApproved
		}
	}

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim inside the create's transaction: a concurrent duplicate blocks
	// here until this one commits, then replays it instead of creating.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payment (
			record_type, payment_status, payment_approval_status, payment_customer_id,
			payment_method, payment_reference_number, payment_date, payment_currency,
			payment_memo, payment_internal_notes,
			payment_amount, payment_applied_total, payment_unapplied_amount,
			payment_owner_id, payment_custom_fields, payment_created_by, payment_updated_by
		) VALUES (
			$1,$2,$3,$4, $5,$6,COALESCE($7, CURRENT_DATE),$8, $9,$10,
			$11,0,$11,
			$12,$13,$14,$14
		) RETURNING payment_id, payment_uuid`,
		typeID, initialStatusID, initialApprovalStatus, custID,
		in.MethodID, in.ReferenceNumber, in.PaymentDate, in.CurrencyID,
		in.Memo, in.InternalNotes,
		in.Amount,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}

	if _, err := assignNumber(ctx, tx, int64(newID)); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, initialStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment create history: %w", err)
	}

	// Same lock order as Apply: this payment (just inserted), then each invoice.
	for _, app := range in.Applications {
		if err := applyTx(ctx, tx, newUUID, app.InvoiceUUID, app.Amount, actorEmployeeID); err != nil {
			return nil, fmt.Errorf("apply inline application to invoice %s: %w", app.InvoiceUUID, err)
		}
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, newUUID); err != nil {
			return nil, err
		}
	}

	if err := auditTx(ctx, tx, auditCreate, newUUID, nil, employeeExtra(actorEmployeeID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create payment: %w", err)
	}
	notifyCreated(ctx, pool, newUUID, newID, actorEmployeeID)
	return Get(ctx, pool, newUUID)
}

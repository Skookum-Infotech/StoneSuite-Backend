package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/invoice"
	"stonesuite-backend/workflow"
)

// quickPayMemo is the memo stamped on every payment created by QuickPay.
const quickPayMemo = "Quick payment via invoice"

// QuickPay is the legacy-endpoint wrapper behind POST /invoices/{uuid}/payment
// (spec AD-5). It creates a payment at status APPV (skipping PEND — this
// single-call endpoint implies the money is already confirmed) and applies it
// to the given invoice in one call, reusing applyTx for the balance math and
// "no silent clamp" overpay rejection. The payment insert and the application
// share ONE transaction, so a failed apply leaves no orphan payment. The new
// payment inherits the invoice's currency. A non-empty idempotencyKey makes
// replays safe: if that key was already recorded for this invoice, nothing is
// inserted and the current invoice is returned. Returns the updated invoice,
// matching the pre-existing response shape callers of this endpoint expect.
func QuickPay(ctx context.Context, pool *pgxpool.Pool, invoiceUUID string, amount float64, idempotencyKey string, actorEmployeeID int) (*invoice.Invoice, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	var customerUUID string
	err := pool.QueryRow(ctx, `
		SELECT c.customer_uuid FROM invoice i JOIN customer c ON c.customer_id = i.invoice_customer_id
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL`, invoiceUUID).Scan(&customerUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve invoice customer: %w", err)
	}
	msg, err := workflow.CustomerNotUsableByUUID(ctx, pool, customerUUID)
	if err != nil {
		return nil, err
	}
	if msg != "" {
		return nil, ClientError{Msg: msg}
	}

	var methodID int
	if err := pool.QueryRow(ctx, `SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'OTHR'`).Scan(&methodID); err != nil {
		return nil, fmt.Errorf("resolve default payment method: %w", err)
	}
	typeID, err := typeIDByCode(ctx, pool, "PYMT")
	if err != nil {
		return nil, err
	}
	appvStatusID, err := statusIDByCode(ctx, pool, typeID, "APPV")
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin quickpay: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Locking the invoice first serializes concurrent replays of one key. The
	// brand-new payment row below is invisible to other transactions, so taking
	// the payment lock after the invoice here cannot form a cycle.
	li, err := invoice.LockForUpdate(ctx, tx, invoiceUUID)
	if err != nil {
		return nil, err
	}
	if idempotencyKey != "" {
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment WHERE idempotency_invoice_id = $1 AND idempotency_key = $2)`,
			li.InternalID, idempotencyKey).Scan(&seen); err != nil {
			return nil, fmt.Errorf("check quickpay idempotency key: %w", err)
		}
		if seen {
			return invoice.Get(ctx, pool, invoiceUUID)
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
			payment_owner_id, payment_custom_fields, payment_created_by, payment_updated_by,
			idempotency_key, idempotency_invoice_id
		) VALUES (
			$1,$2,$3,$4, $5,'',CURRENT_DATE,$6, $7,'',
			$8,0,$8,
			$9,$10,$11,$11,
			$12,$13
		) RETURNING payment_id, payment_uuid`,
		typeID, appvStatusID, approvalchain.StatusApproved, li.CustomerID,
		methodID, li.CurrencyID, quickPayMemo,
		amount,
		nullableInt(actorEmployeeID), map[string]any{}, nullableInt(actorEmployeeID),
		nullableText(idempotencyKey), nullableIDIf(idempotencyKey != "", li.InternalID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert quickpay payment: %w", err)
	}
	if _, err := assignNumber(ctx, tx, int64(newID)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, appvStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert quickpay create history: %w", err)
	}
	if err := applyTx(ctx, tx, newUUID, invoiceUUID, amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit quickpay: %w", err)
	}
	notifyCreated(ctx, pool, newUUID, newID, actorEmployeeID)
	return invoice.Get(ctx, pool, invoiceUUID)
}

// nullableText maps "" to SQL NULL.
func nullableText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullableIDIf returns id when ok, else SQL NULL.
func nullableIDIf(ok bool, id int) any {
	if !ok {
		return nil
	}
	return id
}

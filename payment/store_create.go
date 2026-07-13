package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
// payment number, writes the 'create' history row), then — once that
// transaction has committed — applies each inline application sequentially
// by calling Apply. Applications are NOT part of the header's transaction: a
// later application failing does not roll back the header or earlier
// successful applications (mirrors the QuickPay trade-off, spec AD-5/§8).
// New payments start at PEND.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreatePaymentInput, actorEmployeeID int) (*Payment, error) {
	if in.CustomerUUID == "" {
		return nil, ClientError{Msg: "customerUuid is required."}
	}
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
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

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payment (
			record_type, payment_status, payment_customer_id,
			payment_method, payment_reference_number, payment_date, payment_currency,
			payment_memo, payment_internal_notes,
			payment_amount, payment_applied_total, payment_unapplied_amount,
			payment_owner_id, payment_custom_fields, payment_created_by, payment_updated_by
		) VALUES (
			$1,$2,$3, $4,$5,COALESCE($6, CURRENT_DATE),$7, $8,$9,
			$10,0,$10,
			$11,$12,$13,$13
		) RETURNING payment_id, payment_uuid`,
		typeID, pendStatusID, custID,
		in.MethodID, in.ReferenceNumber, in.PaymentDate, in.CurrencyID,
		in.Memo, in.InternalNotes,
		in.Amount,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE payment SET payment_number = $1 WHERE payment_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set payment number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, pendStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create payment: %w", err)
	}

	for _, app := range in.Applications {
		if _, err := Apply(ctx, pool, newUUID, app.InvoiceUUID, app.Amount, actorEmployeeID); err != nil {
			return nil, fmt.Errorf("apply inline application to invoice %s: %w", app.InvoiceUUID, err)
		}
	}
	return Get(ctx, pool, newUUID)
}

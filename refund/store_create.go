package refund

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
		return ClientError{Msg: "Unknown or inactive refund method."}
	}
	if err != nil {
		return fmt.Errorf("resolve refund method: %w", err)
	}
	return nil
}

// resolveLineagePayment resolves an optional source payment for the header's
// lineage FK (AD-12/AD-2) — no money semantics. Verifies it belongs to the
// same customer as the refund.
func resolveLineagePayment(ctx context.Context, pool *pgxpool.Pool, paymentUUID string, custID int) (*int, error) {
	if paymentUUID == "" {
		return nil, nil
	}
	var id, payCustID int
	err := pool.QueryRow(ctx,
		`SELECT payment_id, payment_customer_id FROM payment WHERE payment_uuid = $1 AND payment_deleted_at IS NULL`,
		paymentUUID).Scan(&id, &payCustID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted payment."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve lineage payment: %w", err)
	}
	if payCustID != custID {
		return nil, ClientError{Msg: "Payment belongs to a different customer than the refund."}
	}
	return &id, nil
}

// resolveLineageCreditMemo mirrors resolveLineagePayment for the credit-memo source.
func resolveLineageCreditMemo(ctx context.Context, pool *pgxpool.Pool, creditMemoUUID string, custID int) (*int, error) {
	if creditMemoUUID == "" {
		return nil, nil
	}
	var id, cmCustID int
	err := pool.QueryRow(ctx,
		`SELECT credit_memo_id, credit_memo_customer_id FROM credit_memo WHERE credit_memo_uuid = $1 AND credit_memo_deleted_at IS NULL`,
		creditMemoUUID).Scan(&id, &cmCustID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted credit memo."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve lineage credit memo: %w", err)
	}
	if cmCustID != custID {
		return nil, ClientError{Msg: "Credit memo belongs to a different customer than the refund."}
	}
	return &id, nil
}

// resolveLineageInvoice mirrors resolveLineagePayment for the invoice reference.
func resolveLineageInvoice(ctx context.Context, pool *pgxpool.Pool, invoiceUUID string, custID int) (*int, error) {
	if invoiceUUID == "" {
		return nil, nil
	}
	var id, invCustID int
	err := pool.QueryRow(ctx,
		`SELECT invoice_id, invoice_customer_id FROM invoice WHERE invoice_uuid = $1 AND invoice_deleted_at IS NULL`,
		invoiceUUID).Scan(&id, &invCustID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve lineage invoice: %w", err)
	}
	if invCustID != custID {
		return nil, ClientError{Msg: "Invoice belongs to a different customer than the refund."}
	}
	return &id, nil
}

// Create inserts a new refund header inside one transaction (resolves the
// customer + method + optional lineage, validates custom fields, inserts the
// row, assigns the refund number, writes the 'create' history row). New
// refunds start at PEND — nothing is authorized to move money yet (AD-5), so
// there is no inline-application step here; compose the refund via
// POST .../apply once it has been approved.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateRefundInput, actorEmployeeID int) (*Refund, error) {
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
	paymentID, err := resolveLineagePayment(ctx, pool, in.PaymentUUID, custID)
	if err != nil {
		return nil, err
	}
	creditMemoID, err := resolveLineageCreditMemo(ctx, pool, in.CreditMemoUUID, custID)
	if err != nil {
		return nil, err
	}
	invoiceID, err := resolveLineageInvoice(ctx, pool, in.InvoiceUUID, custID)
	if err != nil {
		return nil, err
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	typeID, err := typeIDByCode(ctx, pool, "RFND")
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
		return nil, fmt.Errorf("begin create refund: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO refund (
			record_type, refund_status, refund_customer_id,
			refund_payment_id, refund_credit_memo_id, refund_invoice_id,
			refund_method, refund_reference_number, refund_date, refund_currency,
			refund_reason, refund_memo, refund_internal_notes,
			refund_amount, refund_applied_total, refund_unapplied_amount,
			refund_owner_id, refund_custom_fields, refund_created_by, refund_updated_by
		) VALUES (
			$1,$2,$3, $4,$5,$6, $7,$8,COALESCE($9, CURRENT_DATE),$10, $11,$12,$13,
			$14,0,$14,
			$15,$16,$17,$17
		) RETURNING refund_id, refund_uuid`,
		typeID, pendStatusID, custID,
		paymentID, creditMemoID, invoiceID,
		in.MethodID, in.ReferenceNumber, in.RefundDate, in.CurrencyID,
		in.Reason, in.Memo, in.InternalNotes,
		in.Amount,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert refund: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE refund SET refund_number = $1 WHERE refund_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set refund number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO refund_history (refund_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, pendStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert refund create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create refund: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

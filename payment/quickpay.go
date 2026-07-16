package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
)

// QuickPay is the legacy-endpoint wrapper behind POST /invoices/{uuid}/payment
// (spec AD-5). It creates a payment at status APPV (skipping PEND — this
// single-call endpoint implies the money is already confirmed) and applies it
// to the given invoice in one call, reusing Apply for the balance math and
// "no silent clamp" overpay rejection. Returns the updated invoice, matching
// the pre-existing response shape callers of this endpoint expect.
func QuickPay(ctx context.Context, pool *pgxpool.Pool, invoiceUUID string, amount float64, actorEmployeeID int) (*invoice.Invoice, error) {
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

	var methodID int
	if err := pool.QueryRow(ctx, `SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'OTHR'`).Scan(&methodID); err != nil {
		return nil, fmt.Errorf("resolve default payment method: %w", err)
	}

	p, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: customerUUID, MethodID: methodID, Amount: amount,
		Memo: "Quick payment via invoice", ReferenceNumber: "",
	}, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	typeID, err := typeIDByCode(ctx, pool, "PYMT")
	if err != nil {
		return nil, err
	}
	appvStatusID, err := statusIDByCode(ctx, pool, typeID, "APPV")
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `UPDATE payment SET payment_status = $1 WHERE payment_uuid = $2`, appvStatusID, p.ID); err != nil {
		return nil, fmt.Errorf("promote quickpay to APPV: %w", err)
	}

	if _, err := Apply(ctx, pool, p.ID, invoiceUUID, amount, actorEmployeeID); err != nil {
		return nil, err
	}
	return invoice.Get(ctx, pool, invoiceUUID)
}

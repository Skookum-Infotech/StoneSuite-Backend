// vendorpayment/refund.go — spec §8 "Refunding", AD-5: a separate ledger
// table netted against applications at recompute time, not a mutation of the
// application row.
package vendorpayment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/vendorbill"
)

// capForRefund returns the portion of a live application's amount that is
// still eligible to be refunded: the application amount minus what has
// already been refunded against it, floored at zero. Pure, side-effect-free.
func capForRefund(applicationAmount, alreadyRefunded float64) float64 {
	c := round2(applicationAmount - alreadyRefunded)
	if c < 0 {
		return 0
	}
	return c
}

// RecordRefund inserts a vendor_payment_refund row against the live
// application between vendorPaymentUUID and vendorBillUUID (spec AD-5).
// Capped at application_amount minus what has already been refunded against
// it; rejects (400) rather than clamping if amount exceeds that cap. No
// status gate on either side.
func RecordRefund(ctx context.Context, pool *pgxpool.Pool, vendorPaymentUUID, vendorBillUUID string, amount float64, reason, referenceNumber, memo string, actorEmployeeID int) (*VendorPayment, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin record refund: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockVendorPaymentForUpdate(ctx, tx, vendorPaymentUUID) // lock order: payment first (AD-11)
	if err != nil {
		return nil, err
	}
	li, err := vendorbill.LockForUpdate(ctx, tx, vendorBillUUID) // then bill (AD-11)
	if err != nil {
		return nil, err
	}

	var applicationAmount float64
	err = tx.QueryRow(ctx, `
		SELECT application_amount FROM vendor_payment_application
		WHERE vendor_payment_id = $1 AND vendor_bill_id = $2 AND application_deleted_at IS NULL`,
		lp.internalID, li.InternalID).Scan(&applicationAmount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "No live application between this payment and vendor bill."}
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor payment application for refund: %w", err)
	}

	var alreadyRefunded float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(refund_amount), 0) FROM vendor_payment_refund
		WHERE vendor_payment_id = $1 AND vendor_bill_id = $2 AND refund_deleted_at IS NULL`,
		lp.internalID, li.InternalID).Scan(&alreadyRefunded); err != nil {
		return nil, fmt.Errorf("sum vendor payment refunds: %w", err)
	}
	capAmt := capForRefund(applicationAmount, alreadyRefunded)
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Refund exceeds the applied amount available to refund."}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_refund (vendor_payment_id, vendor_bill_id, refund_amount, refund_reason, refund_reference_number, refund_memo, refund_created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		lp.internalID, li.InternalID, round2(amount), reason, referenceNumber, memo, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment refund: %w", err)
	}

	if err := recomputeVendorPayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := vendorbill.RecomputeBalance(ctx, tx, li, "refund", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT vendor_payment_id, vendor_payment_status, vendor_payment_status, 'refund', $2 FROM vendor_payment WHERE vendor_payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment refund history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit record refund: %w", err)
	}
	return Get(ctx, pool, vendorPaymentUUID)
}

// RemoveRefund soft-deletes a live refund row ("un-refund"/correction) —
// the mirror operation of RecordRefund, recomputing both rollups back
// upward. No status gate. The refund must belong to vendorPaymentUUID
// (ErrNotFound otherwise).
func RemoveRefund(ctx context.Context, pool *pgxpool.Pool, vendorPaymentUUID, refundUUID string, actorEmployeeID int) (*VendorPayment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin remove refund: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockVendorPaymentForUpdate(ctx, tx, vendorPaymentUUID) // lock order: payment first (AD-11)
	if err != nil {
		return nil, err
	}

	var refundInternalID, vendorBillInternalID int
	err = tx.QueryRow(ctx, `
		SELECT refund_id, vendor_bill_id FROM vendor_payment_refund
		WHERE refund_uuid = $1 AND vendor_payment_id = $2 AND refund_deleted_at IS NULL`,
		refundUUID, lp.internalID).Scan(&refundInternalID, &vendorBillInternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve vendor payment refund: %w", err)
	}

	li, err := vendorbill.LockForUpdateByID(ctx, tx, vendorBillInternalID) // then bill (AD-11)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_payment_refund SET refund_deleted_at = NOW(), refund_deleted_by = $1
		WHERE refund_id = $2`, actorOrSystem(actorEmployeeID), refundInternalID); err != nil {
		return nil, fmt.Errorf("soft-delete vendor payment refund: %w", err)
	}

	if err := recomputeVendorPayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := vendorbill.RecomputeBalance(ctx, tx, li, "unrefund", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT vendor_payment_id, vendor_payment_status, vendor_payment_status, 'unrefund', $2 FROM vendor_payment WHERE vendor_payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment unrefund history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit remove refund: %w", err)
	}
	return Get(ctx, pool, vendorPaymentUUID)
}

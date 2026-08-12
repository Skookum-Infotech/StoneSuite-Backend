// vendorpayment/apply.go — spec §8 "Applying"/"Unapplying": the
// payment<->bill application ledger, a near-verbatim port of
// payment/apply.go with invoice -> vendorbill and customer -> vendor.
package vendorpayment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/vendorbill"
)

// lockedVendorPayment is a row-locked live vendor payment, loaded inside a
// transaction by lockVendorPaymentForUpdate.
type lockedVendorPayment struct {
	internalID int
	vendorID   int
	statusCode string
	amount     float64
}

// lockVendorPaymentForUpdate loads and row-locks a live vendor payment by
// uuid inside tx.
//
// Lock order is a global invariant (spec AD-11): vendor_payment <
// vendor_bill. This must always be called before vendorbill.LockForUpdate /
// LockForUpdateByID, never the reverse, anywhere in this package.
func lockVendorPaymentForUpdate(ctx context.Context, tx pgx.Tx, vendorPaymentUUID string) (lockedVendorPayment, error) {
	var lp lockedVendorPayment
	err := tx.QueryRow(ctx, `
		SELECT vp.vendor_payment_id, vp.vendor_payment_vendor_id, rs.record_status_code, vp.vendor_payment_amount
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_uuid = $1 AND vp.vendor_payment_deleted_at IS NULL
		FOR UPDATE OF vp`, vendorPaymentUUID,
	).Scan(&lp.internalID, &lp.vendorID, &lp.statusCode, &lp.amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedVendorPayment{}, ErrNotFound
	}
	if err != nil {
		return lockedVendorPayment{}, fmt.Errorf("lock vendor payment: %w", err)
	}
	return lp, nil
}

// recomputeVendorPayment recomputes and stores
// vendor_payment_applied_total/unapplied_amount from the live
// vendor_payment_application ledger net the live vendor_payment_refund
// ledger, floored at zero, inside tx.
func recomputeVendorPayment(ctx context.Context, tx pgx.Tx, internalID int, amount float64, actorEmployeeID int) error {
	var applied, refunded float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM vendor_payment_application
		WHERE vendor_payment_id = $1 AND application_deleted_at IS NULL`, internalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum vendor payment applications: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(refund_amount), 0) FROM vendor_payment_refund
		WHERE vendor_payment_id = $1 AND refund_deleted_at IS NULL`, internalID).Scan(&refunded); err != nil {
		return fmt.Errorf("sum vendor payment refunds: %w", err)
	}
	appliedTotal := round2(applied - refunded)
	if appliedTotal < 0 {
		appliedTotal = 0
	}
	unapplied := round2(amount - appliedTotal)
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_payment SET vendor_payment_applied_total = $1, vendor_payment_unapplied_amount = $2,
			vendor_payment_updated_at = NOW(), vendor_payment_updated_by = $3, vendor_payment_record_version = vendor_payment_record_version + 1
		WHERE vendor_payment_id = $4`, appliedTotal, unapplied, nullableInt(actorEmployeeID), internalID); err != nil {
		return fmt.Errorf("update vendor payment rollup: %w", err)
	}
	return nil
}

// capForApply returns the amount of a payment's unapplied balance that can
// actually be applied to a bill: the smaller of the payment's unapplied
// balance and the bill's outstanding balance. Pure, side-effect-free.
func capForApply(unapplied, billBalance float64) float64 {
	if billBalance < unapplied {
		return billBalance
	}
	return unapplied
}

// Apply allocates amount of vendorPaymentUUID's unapplied balance to
// vendorBillUUID. Caps at min(payment.unapplied_amount, bill.BalanceDue());
// rejects (400) rather than clamping if amount exceeds that cap. Rejects
// (409) if the payment is VOID or the bill isn't in a payable status, and
// (400) on a vendor mismatch.
func Apply(ctx context.Context, pool *pgxpool.Pool, vendorPaymentUUID, vendorBillUUID string, amount float64, actorEmployeeID int) (*VendorPayment, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockVendorPaymentForUpdate(ctx, tx, vendorPaymentUUID) // lock order: payment first (AD-11)
	if err != nil {
		return nil, err
	}
	if lp.statusCode == "VOID" {
		return nil, ClientError{Msg: "Cannot apply a voided vendor payment."}
	}
	li, err := vendorbill.LockForUpdate(ctx, tx, vendorBillUUID) // then bill (AD-11)
	if err != nil {
		return nil, err
	}
	if li.VendorID != lp.vendorID {
		return nil, ClientError{Msg: "Vendor bill belongs to a different vendor than the payment."}
	}
	if !vendorbill.PayableStatuses[li.StatusCode] {
		return nil, ClientError{Msg: "Cannot apply payment to a " + li.StatusCode + " vendor bill; it must be approved first."}
	}

	var applied float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(application_amount),0) FROM vendor_payment_application WHERE vendor_payment_id = $1 AND application_deleted_at IS NULL`, lp.internalID).Scan(&applied); err != nil {
		return nil, fmt.Errorf("sum vendor payment applications: %w", err)
	}
	unapplied := round2(lp.amount - applied)
	capAmt := capForApply(unapplied, li.BalanceDue())
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available balance."}
	}

	var existingID int
	err = tx.QueryRow(ctx, `SELECT application_id FROM vendor_payment_application WHERE vendor_payment_id = $1 AND vendor_bill_id = $2 AND application_deleted_at IS NULL`,
		lp.internalID, li.InternalID).Scan(&existingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_payment_application (vendor_payment_id, vendor_bill_id, application_amount, application_created_by)
			VALUES ($1,$2,$3,$4)`, lp.internalID, li.InternalID, round2(amount), nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert vendor payment application: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("check existing vendor payment application: %w", err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_payment_application SET application_amount = application_amount + $1, application_record_version = application_record_version + 1
			WHERE application_id = $2`, round2(amount), existingID); err != nil {
			return nil, fmt.Errorf("increase vendor payment application: %w", err)
		}
	}

	if err := recomputeVendorPayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := vendorbill.RecomputeBalance(ctx, tx, li, "apply", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT vendor_payment_id, vendor_payment_status, vendor_payment_status, 'apply', $2 FROM vendor_payment WHERE vendor_payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment apply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, vendorPaymentUUID)
}

// Unapply reverses the live application between vendorPaymentUUID and
// vendorBillUUID (soft-deletes it), recomputing both rollups. No status gate
// on either side: a reversal must always be possible regardless of the
// payment's or bill's current status.
func Unapply(ctx context.Context, pool *pgxpool.Pool, vendorPaymentUUID, vendorBillUUID string, actorEmployeeID int) (*VendorPayment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin unapply: %w", err)
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

	tag, err := tx.Exec(ctx, `
		UPDATE vendor_payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE vendor_payment_id = $2 AND vendor_bill_id = $3 AND application_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), lp.internalID, li.InternalID)
	if err != nil {
		return nil, fmt.Errorf("unapply: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this payment and vendor bill."}
	}

	if err := recomputeVendorPayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := vendorbill.RecomputeBalance(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT vendor_payment_id, vendor_payment_status, vendor_payment_status, 'unapply', $2 FROM vendor_payment WHERE vendor_payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment unapply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit unapply: %w", err)
	}
	return Get(ctx, pool, vendorPaymentUUID)
}

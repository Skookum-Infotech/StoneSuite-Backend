package vendorbill

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

// PayableStatuses are the only vendor bill statuses a vendor payment may be
// applied against (spec AD-15). ODUE derivation is explicitly out of scope
// for this build, so it is deliberately excluded here.
var PayableStatuses = map[string]bool{"APPV": true, "PART": true}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// Locked is a row-locked live vendor bill, loaded inside a transaction by
// LockForUpdate. It carries the inputs to the AP balance identity so callers
// can gate on the live balance without re-querying.
type Locked struct {
	InternalID int
	VendorID   int
	StatusCode string
	GrandTotal float64
	AmountPaid float64
}

// BalanceDue is the vendor bill's live outstanding balance: grand_total -
// amount_paid, floored at zero. There is no credit-memo leg on the AP side
// (spec §8), unlike invoice.Locked.BalanceDue.
func (l Locked) BalanceDue() float64 {
	b := round2(l.GrandTotal - l.AmountPaid)
	if b < 0 {
		return 0
	}
	return b
}

// LockForUpdate loads and row-locks a live vendor bill by uuid inside tx.
//
// Lock order is a global invariant (spec AD-11): vendor_payment <
// vendor_bill. Callers must already hold their payment's lock before calling
// this, so vendor_bill is always taken last and no cycle -- hence no
// deadlock -- is possible.
func LockForUpdate(ctx context.Context, tx pgx.Tx, vendorBillUUID string) (Locked, error) {
	var l Locked
	err := tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, vb.vendor_bill_vendor_id, rs.record_status_code,
		       vb.vendor_bill_grand_total, vb.vendor_bill_amount_paid
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, vendorBillUUID,
	).Scan(&l.InternalID, &l.VendorID, &l.StatusCode, &l.GrandTotal, &l.AmountPaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locked{}, ClientError{Msg: "Unknown or deleted vendor bill."}
	}
	if err != nil {
		return Locked{}, fmt.Errorf("lock vendor bill: %w", err)
	}
	return l, nil
}

// LockForUpdateByID is LockForUpdate keyed on the internal serial id, for
// cascades that already hold a list of affected vendor bill ids. Callers must
// iterate those ids in ascending order so concurrent cascades touching the
// same bills cannot lock them in opposite orders and deadlock.
func LockForUpdateByID(ctx context.Context, tx pgx.Tx, internalID int) (Locked, error) {
	l := Locked{InternalID: internalID}
	err := tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_vendor_id, rs.record_status_code,
		       vb.vendor_bill_grand_total, vb.vendor_bill_amount_paid
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_id = $1
		FOR UPDATE OF vb`, internalID,
	).Scan(&l.VendorID, &l.StatusCode, &l.GrandTotal, &l.AmountPaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locked{}, ClientError{Msg: "Unknown or deleted vendor bill."}
	}
	if err != nil {
		return Locked{}, fmt.Errorf("lock vendor bill by id: %w", err)
	}
	return l, nil
}

// DeriveStatus re-derives a vendor bill's status purely from what has been
// paid against it.
//
// This intentionally does NOT go through CanTransition: that map is for
// user-directed transitions and has no path back out of PAID, or from PART to
// APPV -- moves an Unapply legitimately needs.
func DeriveStatus(currentCode string, amountPaid, grandTotal float64) string {
	balanceDue := grandTotal - amountPaid
	switch {
	case balanceDue <= 0.005:
		return "PAID"
	case amountPaid > 0.005:
		return "PART"
	case currentCode == "PART" || currentCode == "PAID":
		return "APPV" // fully unapplied back to zero
	default:
		return currentCode
	}
}

// RecomputeBalance is the sole writer of a vendor bill's AP rollup.
//
// It recomputes vendor_bill_amount_paid from the live
// vendor_payment_application ledger net the live vendor_payment_refund
// ledger, derives balance_due and status from it, and writes a
// vendor_bill_history row -- all inside tx.
//
// vendorpayment.Apply/Unapply/RecordRefund all route through this so the
// balance identity exists in exactly one place (spec AD-4).
func RecomputeBalance(ctx context.Context, tx pgx.Tx, l Locked, action string, actorEmployeeID int) error {
	var applied, refunded float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(vpa.application_amount), 0)
		FROM vendor_payment_application vpa
		JOIN vendor_payment vp ON vp.vendor_payment_id = vpa.vendor_payment_id
		WHERE vpa.vendor_bill_id = $1 AND vpa.application_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL`,
		l.InternalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum vendor bill payment applications: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(vpr.refund_amount), 0)
		FROM vendor_payment_refund vpr
		JOIN vendor_payment vp ON vp.vendor_payment_id = vpr.vendor_payment_id
		WHERE vpr.vendor_bill_id = $1 AND vpr.refund_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL`,
		l.InternalID).Scan(&refunded); err != nil {
		return fmt.Errorf("sum vendor bill payment refunds: %w", err)
	}
	amountPaid := round2(applied - refunded)
	if amountPaid < 0 {
		amountPaid = 0
	}

	updated := Locked{GrandTotal: l.GrandTotal, AmountPaid: amountPaid}
	balanceDue := updated.BalanceDue()
	toCode := DeriveStatus(l.StatusCode, amountPaid, l.GrandTotal)

	var vbilTypeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL'`).Scan(&vbilTypeID); err != nil {
		return fmt.Errorf("resolve VBIL type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		vbilTypeID, l.StatusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve vendor bill from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		vbilTypeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve vendor bill to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_amount_paid = $1, vendor_bill_balance_due = $2,
			vendor_bill_status = $3, vendor_bill_updated_at = NOW(), vendor_bill_updated_by = $4,
			vendor_bill_record_version = vendor_bill_record_version + 1
		WHERE vendor_bill_id = $5`,
		amountPaid, balanceDue, toStatusID, nullableInt(actorEmployeeID), l.InternalID); err != nil {
		return fmt.Errorf("update vendor bill rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_history (vendor_bill_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`,
		l.InternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert vendor bill %s history: %w", action, err)
	}
	return nil
}

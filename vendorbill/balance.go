// vendorbill/balance.go — AD-7: the AP balance identity, the accounts-
// payable mirror of invoice/balance.go's AR identity.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PayableStatuses are the only vendor bill statuses that accept a new
// settlement. A bill must be approved before anything can be paid against it.
var PayableStatuses = map[string]bool{"APPV": true, "PART": true, "ODUE": true}

// Locked is a row-locked live vendor bill, loaded inside a transaction by
// LockForUpdate. It carries the two inputs to the AP balance identity so
// callers can gate on the live balance without re-querying.
type Locked struct {
	InternalID int
	VendorID   int
	StatusCode string
	GrandTotal float64
	AmountPaid float64
}

// BalanceDue is the vendor bill's live outstanding balance: grand_total -
// amount_paid, floored at zero.
func (l Locked) BalanceDue() float64 {
	b := round2(l.GrandTotal - l.AmountPaid)
	if b < 0 {
		return 0
	}
	return b
}

// LockForUpdate loads and row-locks a live vendor bill by uuid inside tx.
//
// Lock order (AD-15): vendor_bill_payment < vendor_bill -- a fresh hierarchy
// that does not overlap the AR side's documented credit_memo < payment <
// invoice, so no cycle -- hence no deadlock -- is possible across the two.
func LockForUpdate(ctx context.Context, tx pgx.Tx, billUUID string) (Locked, error) {
	var l Locked
	err := tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, vb.vendor_bill_vendor_id, rs.record_status_code,
		       vb.vendor_bill_grand_total, vb.vendor_bill_amount_paid
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, billUUID,
	).Scan(&l.InternalID, &l.VendorID, &l.StatusCode, &l.GrandTotal, &l.AmountPaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locked{}, ClientError{Msg: "Unknown or deleted vendor bill."}
	}
	if err != nil {
		return Locked{}, fmt.Errorf("lock vendor bill: %w", err)
	}
	return l, nil
}

// DeriveStatus re-derives a vendor bill's status purely from what has been
// settled against it (AD-7).
//
// This intentionally does NOT go through CanTransition: that map is for
// user-directed transitions and has no path back out of PAID, or from PART
// to APPV -- moves an unapply legitimately needs.
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

// RecomputeBalance is the sole writer of a vendor bill's AP rollup (AD-7).
//
// It recomputes vendor_bill_amount_paid from the live vendor_bill_payment
// ledger, derives balance_due and status, and writes a vendor_bill_history
// row -- all inside tx. RecordPayment and RemovePayment both route through
// this so the balance identity exists in exactly one place.
func RecomputeBalance(ctx context.Context, tx pgx.Tx, l Locked, action string, actorEmployeeID int) error {
	var amountPaid float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM vendor_bill_payment
		WHERE vendor_bill_id = $1 AND deleted_at IS NULL`,
		l.InternalID).Scan(&amountPaid); err != nil {
		return fmt.Errorf("sum vendor bill payments: %w", err)
	}
	amountPaid = round2(amountPaid)

	updated := Locked{GrandTotal: l.GrandTotal, AmountPaid: amountPaid}
	balanceDue := updated.BalanceDue()
	toCode := DeriveStatus(l.StatusCode, amountPaid, l.GrandTotal)

	var vbTypeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL'`).Scan(&vbTypeID); err != nil {
		return fmt.Errorf("resolve VBIL type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		vbTypeID, l.StatusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve vendor bill from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		vbTypeID, toCode).Scan(&toStatusID); err != nil {
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

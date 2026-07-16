package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PayableStatuses are the only invoice statuses that accept new settlement --
// whether cash (payment.Apply) or credit (creditmemo.Apply). An invoice must be
// sent before anything can be applied against it.
var PayableStatuses = map[string]bool{"SENT": true, "PART": true, "ODUE": true}

// Locked is a row-locked live invoice, loaded inside a transaction by
// LockForUpdate. It carries the three inputs to the AR balance identity so
// callers can gate on the live balance without re-querying.
type Locked struct {
	InternalID  int
	CustomerID  int
	StatusCode  string
	GrandTotal  float64
	AmountPaid  float64
	CreditTotal float64
}

// BalanceDue is the invoice's live outstanding balance:
//
//	grand_total - amount_paid - credit_total
//
// floored at zero. AmountPaid is cash only; CreditTotal is credit-memo
// application. Keeping them separate is what makes "how much did we actually
// collect?" answerable from the invoice row (spec AD-4).
func (l Locked) BalanceDue() float64 {
	b := round2(l.GrandTotal - l.AmountPaid - l.CreditTotal)
	if b < 0 {
		return 0
	}
	return b
}

// settled is the total value discharged against the invoice by any means.
func (l Locked) settled() float64 { return round2(l.AmountPaid + l.CreditTotal) }

// LockForUpdate loads and row-locks a live invoice by uuid inside tx.
//
// Lock order is a global invariant: credit_memo < payment < invoice. Callers
// must already hold their own document's lock before calling this, so that
// invoice is always taken last and no cycle -- hence no deadlock -- is possible
// across payment.Apply, creditmemo.Apply, and either void cascade.
func LockForUpdate(ctx context.Context, tx pgx.Tx, invoiceUUID string) (Locked, error) {
	var l Locked
	err := tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_customer_id, rs.record_status_code,
		       i.invoice_grand_total, i.invoice_amount_paid, i.invoice_credit_total
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, invoiceUUID,
	).Scan(&l.InternalID, &l.CustomerID, &l.StatusCode, &l.GrandTotal, &l.AmountPaid, &l.CreditTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locked{}, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return Locked{}, fmt.Errorf("lock invoice: %w", err)
	}
	return l, nil
}

// LockForUpdateByID is LockForUpdate keyed on the internal serial id, for void
// cascades that already hold a list of affected invoice ids. Callers must
// iterate those ids in ascending order so concurrent cascades touching the same
// invoices cannot lock them in opposite orders and deadlock.
func LockForUpdateByID(ctx context.Context, tx pgx.Tx, internalID int) (Locked, error) {
	l := Locked{InternalID: internalID}
	err := tx.QueryRow(ctx, `
		SELECT i.invoice_customer_id, rs.record_status_code,
		       i.invoice_grand_total, i.invoice_amount_paid, i.invoice_credit_total
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_id = $1
		FOR UPDATE OF i`, internalID,
	).Scan(&l.CustomerID, &l.StatusCode, &l.GrandTotal, &l.AmountPaid, &l.CreditTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locked{}, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return Locked{}, fmt.Errorf("lock invoice by id: %w", err)
	}
	return l, nil
}

// DeriveStatus re-derives an invoice's status purely from what has been settled
// against it, where settled = amount_paid + credit_total.
//
// This intentionally does NOT go through CanTransition: that map is for
// user-directed transitions and has no path back out of PAID, or from PART to
// SENT -- moves an Unapply legitimately needs.
func DeriveStatus(currentCode string, settled, grandTotal float64) string {
	balanceDue := grandTotal - settled
	switch {
	case balanceDue <= 0.005:
		return "PAID"
	case settled > 0.005:
		return "PART"
	case currentCode == "PART" || currentCode == "PAID":
		return "SENT" // fully unapplied back to zero; ODUE re-flagging is a separate concern
	default:
		return currentCode
	}
}

// RecomputeBalance is the sole writer of an invoice's AR rollup.
//
// It recomputes invoice_amount_paid from the live payment_application ledger and
// invoice_credit_total from the live credit_memo_application ledger, derives
// balance_due and status from both, and writes an invoice_history row -- all
// inside tx.
//
// Both payment.Apply and creditmemo.Apply route through this so the balance
// identity exists in exactly one place: two copies of a financial invariant is
// how the two ledgers would silently drift apart.
func RecomputeBalance(ctx context.Context, tx pgx.Tx, l Locked, action string, actorEmployeeID int) error {
	var amountPaid, creditTotal float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(pa.application_amount), 0)
		FROM payment_application pa
		JOIN payment p ON p.payment_id = pa.payment_id
		WHERE pa.invoice_id = $1 AND pa.application_deleted_at IS NULL AND p.payment_deleted_at IS NULL`,
		l.InternalID).Scan(&amountPaid); err != nil {
		return fmt.Errorf("sum invoice payment applications: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(ca.application_amount), 0)
		FROM credit_memo_application ca
		JOIN credit_memo cm ON cm.credit_memo_id = ca.credit_memo_id
		WHERE ca.invoice_id = $1 AND ca.application_deleted_at IS NULL AND cm.credit_memo_deleted_at IS NULL`,
		l.InternalID).Scan(&creditTotal); err != nil {
		return fmt.Errorf("sum invoice credit applications: %w", err)
	}
	amountPaid = round2(amountPaid)
	creditTotal = round2(creditTotal)

	updated := Locked{GrandTotal: l.GrandTotal, AmountPaid: amountPaid, CreditTotal: creditTotal}
	balanceDue := updated.BalanceDue()
	toCode := DeriveStatus(l.StatusCode, updated.settled(), l.GrandTotal)

	var invTypeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'INVC'`).Scan(&invTypeID); err != nil {
		return fmt.Errorf("resolve INVC type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		invTypeID, l.StatusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve invoice from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		invTypeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve invoice to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_amount_paid = $1, invoice_credit_total = $2, invoice_balance_due = $3,
			invoice_status = $4, invoice_updated_at = NOW(), invoice_updated_by = $5,
			invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $6`,
		amountPaid, creditTotal, balanceDue, toStatusID, nullableInt(actorEmployeeID), l.InternalID); err != nil {
		return fmt.Errorf("update invoice rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`,
		l.InternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert invoice %s history: %w", action, err)
	}
	return nil
}

package invoice

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/glpost"
)

// ledgerStatuses are the statuses at or beyond approval, where an invoice's
// revenue is on the books. Draft and Pending Approval are not: nothing has been
// billed yet.
var ledgerStatuses = map[string]bool{"APPV": true, "SENT": true, "PART": true, "PAID": true, "ODUE": true}

// syncLedger brings the general ledger in line with the invoice's status after
// a change, inside the change's own transaction (so a refused posting rolls the
// status change back):
//   - entering a ledger status from a non-ledger one posts the invoice
//     (Dr Accounts Receivable [+ Sales Discount], Cr Sales Revenue + Sales Tax
//     Payable);
//   - a void reverses that entry, if the invoice has one.
//
// prevCode is the status before the change ("" when the invoice is new or the
// caller does not know it, which the approval path never needs: an approval
// always leaves a non-ledger status). Keying on the transition, not just the
// status, is what keeps an invoice approved before posting existed from being
// posted retroactively -- possibly into a closed period -- the next time
// someone moves it from Sent to Overdue.
func syncLedger(ctx context.Context, tx pgx.Tx, internalID, actorEmployeeID int, prevCode string) error {
	var uuid, number, code string
	var date time.Time
	var discount, tax, grand float64
	if err := tx.QueryRow(ctx, `
		SELECT i.invoice_uuid::text, COALESCE(i.invoice_number, ''), rs.record_status_code, i.invoice_date,
		       i.invoice_discount_total, i.invoice_tax_total, i.invoice_grand_total
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_id = $1`, internalID,
	).Scan(&uuid, &number, &code, &date, &discount, &tax, &grand); err != nil {
		return fmt.Errorf("load invoice for ledger: %w", err)
	}
	var err error
	switch {
	case ledgerStatuses[code] && !ledgerStatuses[prevCode]:
		_, err = glpost.PostOnce(ctx, tx, glpost.Posting{
			SourceType: glpost.SourceInvoice, SourceID: uuid, Date: date,
			Memo: "Invoice " + number, Actor: actorEmployeeID,
			Legs: glpost.InvoiceLegs(glpost.InvoiceAmounts{
				Discount: glpost.Cents(discount), Tax: glpost.Cents(tax), Grand: glpost.Cents(grand),
			}),
		})
	case code == statusVoid:
		_, err = glpost.ReverseIfPosted(ctx, tx, glpost.SourceInvoice, uuid, glpost.Today(),
			"Void of invoice "+number, actorEmployeeID)
	}
	if ue, ok := glpost.AsUserError(err); ok {
		return ClientError{Msg: ue.Msg}
	}
	return err
}

package vendorbill

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/glpost"
)

// ledgerStatuses are the statuses at or beyond approval, where a bill's expense
// and the payable it creates are on the books.
var ledgerStatuses = map[string]bool{"APPV": true, "PINI": true, "PART": true, "PAID": true, "ODUE": true}

// ledgerVoid is the terminal void status code.
const ledgerVoid = "VOID"

// syncLedger brings the general ledger in line with the bill's status after a
// change, inside the change's own transaction (so a refused posting rolls the
// status change back):
//   - entering a ledger status from a non-ledger one posts the bill (Dr Cost of
//     Goods Sold + Sales Tax Receivable, Cr Accounts Payable);
//   - a void reverses that entry, if the bill has one.
//
// prevCode is the status before the change ("" when unknown, which the approval
// path never needs). Keying on the transition keeps a bill approved before
// posting existed from being posted retroactively when it is later touched.
func syncLedger(ctx context.Context, tx pgx.Tx, internalID, actorEmployeeID int, prevCode string) error {
	var uuid, number, code string
	var date time.Time
	var tax, grand float64
	if err := tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_uuid::text, COALESCE(vb.vendor_bill_number, ''), rs.record_status_code, vb.vendor_bill_date,
		       vb.vendor_bill_tax_total, vb.vendor_bill_grand_total
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_id = $1`, internalID,
	).Scan(&uuid, &number, &code, &date, &tax, &grand); err != nil {
		return fmt.Errorf("load vendor bill for ledger: %w", err)
	}
	var err error
	switch {
	case ledgerStatuses[code] && !ledgerStatuses[prevCode]:
		_, err = glpost.PostOnce(ctx, tx, glpost.Posting{
			SourceType: glpost.SourceVendorBill, SourceID: uuid, Date: date,
			Memo: "Vendor bill " + number, Actor: actorEmployeeID,
			Legs: glpost.BillLegs(glpost.BillAmounts{Tax: glpost.Cents(tax), Grand: glpost.Cents(grand)}),
		})
	case code == ledgerVoid:
		_, err = glpost.ReverseIfPosted(ctx, tx, glpost.SourceVendorBill, uuid, glpost.Today(),
			"Void of vendor bill "+number, actorEmployeeID)
	}
	if ue, ok := glpost.AsUserError(err); ok {
		return ClientError{Msg: ue.Msg}
	}
	return err
}

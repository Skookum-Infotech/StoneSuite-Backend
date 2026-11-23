package vendorpayment

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/glpost"
)

// ledgerVoid is the terminal void status code.
const ledgerVoid = "VOID"

// syncLedger brings the general ledger in line with the vendor payment's status
// after a change, inside the change's own transaction:
//   - becoming Paid (the money has actually gone out) posts it (Dr Accounts
//     Payable, Cr Bank);
//   - a void reverses that entry, if the payment has one. Paid can still be
//     voided, so this is the path that returns the money to the books.
//
// prevCode is the status before the change ("" when unknown). Keying on the
// transition keeps a payment marked Paid before posting existed from being
// posted retroactively.
func syncLedger(ctx context.Context, tx pgx.Tx, internalID, actorEmployeeID int, prevCode string) error {
	var uuid, number, code string
	var date time.Time
	var amount float64
	if err := tx.QueryRow(ctx, `
		SELECT vp.vendor_payment_uuid::text, COALESCE(vp.vendor_payment_number, ''), rs.record_status_code,
		       vp.vendor_payment_date, vp.vendor_payment_amount
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_id = $1`, internalID,
	).Scan(&uuid, &number, &code, &date, &amount); err != nil {
		return fmt.Errorf("load vendor payment for ledger: %w", err)
	}
	var err error
	switch {
	case code == PaidStatusCode && prevCode != PaidStatusCode:
		_, err = glpost.PostOnce(ctx, tx, glpost.Posting{
			SourceType: glpost.SourceVendorPayment, SourceID: uuid, Date: date,
			Memo: "Vendor payment " + number, Actor: actorEmployeeID,
			Legs: glpost.VendorPaymentLegs(glpost.Cents(amount)),
		})
	case code == ledgerVoid:
		_, err = glpost.ReverseIfPosted(ctx, tx, glpost.SourceVendorPayment, uuid, glpost.Today(),
			"Void of vendor payment "+number, actorEmployeeID)
	}
	if ue, ok := glpost.AsUserError(err); ok {
		return ClientError{Msg: ue.Msg}
	}
	return err
}

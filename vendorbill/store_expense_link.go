// vendorbill/store_expense_link.go — tying a vendor bill to the approved expense
// claim it pays, whether the bill is raised from the claim's own page or made by
// hand on the Vendor Bill form.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// claimExpenseTx resolves the expense claim a bill is being raised from, locks it,
// and checks the bill can stand for it, inside the caller's transaction. It
// returns the claim's internal id for the bill's vendor_bill_expense_id.
//
// A claim can be billed when it is Approved, the bill's vendor is the claimant's
// Employee vendor, and it has no live bill already. A bill that was voided does
// not count: it is released from the claim (its link cleared) so the claim can be
// billed again, and the old bill keeps its own number, notes and history.
func claimExpenseTx(ctx context.Context, tx pgx.Tx, expenseUUID string, vendorInternalID int) (int, error) {
	var expenseID int
	var statusCode string
	var claimantVendorID *int
	err := tx.QueryRow(ctx, `
		SELECT e.expense_id, rs.record_status_code, v.vendor_id
		FROM expense e
		JOIN lkp_record_status rs ON rs.record_status_id = e.expense_status
		LEFT JOIN vendor v ON v.vendor_employee_id = e.expense_claimant_id AND v.vendor_deleted_at IS NULL
		WHERE e.expense_uuid = $1 AND e.expense_deleted_at IS NULL
		FOR UPDATE OF e`, expenseUUID,
	).Scan(&expenseID, &statusCode, &claimantVendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: "Unknown expense claim."}
	}
	if err != nil {
		return 0, fmt.Errorf("load expense claim for bill: %w", err)
	}
	if statusCode != expenseApprovedStatusCode {
		return 0, ClientError{Msg: "Only an approved expense claim can be billed."}
	}
	if claimantVendorID == nil || *claimantVendorID != vendorInternalID {
		return 0, ClientError{Msg: "A bill for an expense claim must be to the claimant's own employee vendor."}
	}

	var billed bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM vendor_bill b
			JOIN lkp_record_status s ON s.record_status_id = b.vendor_bill_status
			WHERE b.vendor_bill_expense_id = $1 AND b.vendor_bill_deleted_at IS NULL
			  AND s.record_status_code <> $2)`, expenseID, voidStatusCode).Scan(&billed); err != nil {
		return 0, fmt.Errorf("check expense claim bills: %w", err)
	}
	if billed {
		return 0, ClientError{Msg: "This expense claim already has a vendor bill."}
	}

	if err := releaseVoidedBillsTx(ctx, tx, expenseID); err != nil {
		return 0, err
	}
	return expenseID, nil
}

// releaseVoidedBillsTx frees a claim from the voided bills that still point at
// it: one live bill per claim is a unique index over every undeleted bill,
// voided ones included, so a voided bill has to let go before the claim can be
// billed again.
func releaseVoidedBillsTx(ctx context.Context, tx pgx.Tx, expenseID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_expense_id = NULL
		WHERE vendor_bill_expense_id = $1 AND vendor_bill_status IN (
			SELECT s.record_status_id FROM lkp_record_status s
			JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
			WHERE t.record_type_code = $3 AND s.record_status_code = $2)`,
		expenseID, voidStatusCode, vbilRecordTypeCode); err != nil {
		return fmt.Errorf("release voided expense bill: %w", err)
	}
	return nil
}

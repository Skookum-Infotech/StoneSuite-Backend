// vendorbill/store_create_expense.go — the vendor bill an approved expense claim
// raises, and the sync that marks the claim Paid when the bill is.
package vendorbill

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ExpenseLine is one expense-claim line carried onto the bill as one bill line.
type ExpenseLine struct {
	Description string
	Amount      float64
}

// ExpenseBillInput is what an approved expense claim hands over to raise its bill.
type ExpenseBillInput struct {
	ExpenseID     int    // the claim's internal id, stored on the bill as its source
	VendorUUID    string // the claimant's Employee vendor
	ExpenseNumber string // becomes the bill's vendor invoice / reference number
	Memo          string
	// OwnerEmployeeID owns the bill (the employee whose approval raised it).
	OwnerEmployeeID int
	Lines           []ExpenseLine
}

// CreateFromExpenseTx inserts a Draft vendor bill for an approved expense claim
// inside the caller's transaction and returns its external uuid. The bill then
// follows the ordinary vendor bill workflow (approval, payment). Each claim line
// becomes one bill line at quantity 1; the claim's number is the bill's vendor
// invoice number. The caller owns commit and rollback.
func CreateFromExpenseTx(ctx context.Context, tx pgx.Tx, in ExpenseBillInput, actorEmployeeID int) (string, error) {
	if len(in.Lines) == 0 {
		return "", ClientError{Msg: "The expense claim has no lines to bill."}
	}
	items := make([]LineInput, len(in.Lines))
	for i, l := range in.Lines {
		items[i] = LineInput{LineNumber: i + 1, Description: l.Description, Quantity: 1, UnitPrice: l.Amount}
	}
	if err := validateWriteInput(items, 0, 0, "", ""); err != nil {
		return "", err
	}

	vendorInternalID, vendorName, err := vendorSnapshot(ctx, tx, in.VendorUUID)
	if err != nil {
		return "", err
	}
	lines, err := resolveLines(ctx, tx, items, 0)
	if err != nil {
		return "", err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, 0, 0)

	if err := releaseVoidedBillsTx(ctx, tx, in.ExpenseID); err != nil {
		return "", err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return "", fmt.Errorf("resolve VBIL record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return "", fmt.Errorf("resolve DRFT status: %w", err)
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_bill_status", draftStatusID, ""},
		{"vendor_bill_vendor_id", vendorInternalID, ""},
		{"vendor_bill_vendor_name", vendorName, ""},
		{"vendor_bill_vendor_invoice_number", in.ExpenseNumber, ""},
		{"vendor_bill_reference_number", in.ExpenseNumber, ""},
		{"vendor_bill_date", orNow(""), "::date"},
		{"vendor_bill_memo", in.Memo, ""},
		{"vendor_bill_notes", strings.TrimSpace("Raised from expense claim " + in.ExpenseNumber + "."), ""},
		{"vendor_bill_owner_id", nullableInt(in.OwnerEmployeeID), ""},
		{"vendor_bill_subtotal", header.Subtotal, ""},
		{"vendor_bill_discount_total", header.DiscountTotal, ""},
		{"vendor_bill_tax_total", header.TaxTotal, ""},
		{"vendor_bill_adjustment", 0, ""},
		{"vendor_bill_grand_total", header.GrandTotal, ""},
		{"vendor_bill_amount_paid", header.AmountPaid, ""},
		{"vendor_bill_balance_due", header.BalanceDue, ""},
		{"vendor_bill_custom_fields", map[string]any{}, ""},
		{"vendor_bill_created_by", nullableInt(actorEmployeeID), ""},
		{"vendor_bill_expense_id", in.ExpenseID, ""},
	}
	insertSQL, insertArgs := buildInsert("vendor_bill", cv, "vendor_bill_id, vendor_bill_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		return "", fmt.Errorf("insert vendor bill from expense: %w", err)
	}
	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return "", err
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return "", err
	}
	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)
	if err := auditTx(ctx, tx, auditCreate, newUUID, nil, map[string]any{"source": "expense"}); err != nil {
		return "", err
	}
	return newUUID, nil
}

// Expense status codes the sync below moves a claim between.
const (
	expenseApprovedStatusCode = "APPV"
	expensePaidStatusCode     = "PAID"
)

// syncExpenseTx keeps the expense claim a bill was raised from in step with the
// bill's payment: the claim is Paid while the bill is, and goes back to Approved
// if the bill stops being paid (a payment voided or refunded). It does nothing
// for a bill that did not come from a claim. Called from RecomputeBalance, the
// sole writer of a bill's paid state, inside its transaction.
func syncExpenseTx(ctx context.Context, tx pgx.Tx, billInternalID int, billStatusCode string, actorEmployeeID int) error {
	var expenseID *int
	if err := tx.QueryRow(ctx,
		`SELECT vendor_bill_expense_id FROM vendor_bill WHERE vendor_bill_id = $1`, billInternalID).Scan(&expenseID); err != nil {
		return fmt.Errorf("load vendor bill expense link: %w", err)
	}
	if expenseID == nil {
		return nil
	}
	fromCode, toCode := expenseApprovedStatusCode, expensePaidStatusCode
	if billStatusCode != paidStatusCode {
		fromCode, toCode = expensePaidStatusCode, expenseApprovedStatusCode
	}
	var fromID, toID int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT rs.record_status_id FROM lkp_record_status rs
		   JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		   WHERE rt.record_type_code = 'EXPN' AND rs.record_status_code = $1),
		  (SELECT rs.record_status_id FROM lkp_record_status rs
		   JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		   WHERE rt.record_type_code = 'EXPN' AND rs.record_status_code = $2)`,
		fromCode, toCode).Scan(&fromID, &toID); err != nil {
		return fmt.Errorf("resolve expense statuses: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE expense SET expense_status = $2, expense_updated_at = NOW(), expense_updated_by = $3,
			expense_record_version = expense_record_version + 1
		WHERE expense_id = $1 AND expense_status = $4 AND expense_deleted_at IS NULL`,
		*expenseID, toID, nullableInt(actorEmployeeID), fromID)
	if err != nil {
		return fmt.Errorf("sync expense status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO expense_history (expense_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,'transition',$4)`,
		*expenseID, fromID, toID, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert expense history: %w", err)
	}
	return nil
}

// vendorbill/store_create.go
package vendorbill

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Create inserts a new vendor bill (header + lines) inside one transaction:
// snapshots the vendor name (AD-2), resolves and prices every line (AD-3,
// AD-9), computes header totals, assigns the VBIL number (AD-10), and starts
// the bill at DRFT (AD-5). CustomFields is validated and nil-guarded before
// the insert -- vendor_bill_custom_fields is NOT NULL DEFAULT '{}'.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateVendorBillInput, actorEmployeeID int) (*VendorBill, error) {
	if strings.TrimSpace(in.VendorUUID) == "" {
		return nil, ClientError{Msg: "A vendor is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	vendorInternalID, vendorName, err := vendorSnapshot(ctx, tx, in.VendorUUID)
	if err != nil {
		return nil, err
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.Adjustment, 0)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_bill_status", draftStatusID, ""},
		{"vendor_bill_vendor_id", vendorInternalID, ""},
		{"vendor_bill_vendor_name", vendorName, ""},
		{"vendor_bill_vendor_invoice_number", in.VendorInvoiceNumber, ""},
		{"vendor_bill_reference_number", in.ReferenceNumber, ""},
		{"vendor_bill_date", orNow(in.BillDate), "::date"},
		{"vendor_bill_due_date", nullableDate(in.DueDate), "::date"},
		{"vendor_bill_payment_terms", in.PaymentTermsID, ""},
		{"vendor_bill_currency", in.CurrencyID, ""},
		{"vendor_bill_sales_tax_percent", in.SalesTaxPercent, ""},
		{"vendor_bill_memo", in.Memo, ""},
		{"vendor_bill_notes", in.Notes, ""},
		{"vendor_bill_internal_notes", in.InternalNotes, ""},
		{"vendor_bill_terms_conditions", in.TermsConditions, ""},
		{"vendor_bill_owner_id", nullableInt(ownerEmployeeID), ""},
		{"vendor_bill_subtotal", header.Subtotal, ""},
		{"vendor_bill_discount_total", header.DiscountTotal, ""},
		{"vendor_bill_tax_total", header.TaxTotal, ""},
		{"vendor_bill_adjustment", in.Adjustment, ""},
		{"vendor_bill_grand_total", header.GrandTotal, ""},
		{"vendor_bill_amount_paid", header.AmountPaid, ""},
		{"vendor_bill_balance_due", header.BalanceDue, ""},
		{"vendor_bill_custom_fields", custom, ""},
		{"vendor_bill_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("vendor_bill", cv, "vendor_bill_id, vendor_bill_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, currency, or vendor) does not exist."}
		}
		return nil, fmt.Errorf("insert vendor bill: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx, `UPDATE vendor_bill SET vendor_bill_number = $1 WHERE vendor_bill_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set vendor bill number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create vendor bill: %w", err)
	}
	notifyCreated(ctx, pool, newUUID, internalID, actorEmployeeID)
	return Get(ctx, pool, newUUID)
}

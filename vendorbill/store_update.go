// vendorbill/store_update.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live vendor bill's header fields and lines (recomputing
// totals) inside one transaction. Allowed only at DRFT (AD-12) -- once
// submitted for approval, recall it to draft (PAPV->DRFT) to revise.
// DRFT bills never have payments (AD-7 gates settlement to APPV/PART/ODUE),
// so amountPaid is always 0 here -- no "can't reduce below what's paid"
// guard is needed, unlike invoice's Update.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateVendorBillInput, actorEmployeeID int) (*VendorBill, error) {
	if err := validateWriteInput(in.Items, in.SalesTaxPercent, in.Adjustment, in.BillDate, in.DueDate); err != nil {
		return nil, err
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, vendorInternalID int
	var statusCode, currentInvoice string
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, rs.record_status_code, vb.vendor_bill_vendor_id, vb.vendor_bill_vendor_invoice_number,
		       vb.vendor_bill_record_version
		FROM vendor_bill vb JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, uuid,
	).Scan(&internalID, &statusCode, &vendorInternalID, &currentInvoice, &currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor bill for update: %w", err)
	}
	// Version first, under the row lock: a stale form is a conflict, not an
	// "only drafts can be edited" error caused by someone else's transition.
	if err := checkExpectedVersion(in.ExpectedVersion, currentVersion); err != nil {
		return nil, err
	}
	if statusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft vendor bill can be edited. Recall it to draft first."}
	}
	// Captured under the row lock so the audit "old" value is the row this
	// update actually replaces.
	before, err := getQ(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}

	// A changed, non-empty invoice number must not collide with another live
	// bill of the same vendor (the bill itself is excluded).
	dupUUID := ""
	if norm := NormalizeDocNumber(in.VendorInvoiceNumber); norm != "" && norm != NormalizeDocNumber(currentInvoice) {
		if dupUUID, _, err = guardDuplicateInvoice(ctx, tx, vendorInternalID, in.VendorInvoiceNumber, in.AllowDuplicate, uuid); err != nil {
			return nil, err
		}
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
	if header.GrandTotal < 0 {
		return nil, ClientError{Msg: "The vendor bill total cannot be negative."}
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
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
		{"vendor_bill_owner_id", in.OwnerEmployeeID, ""},
		{"vendor_bill_subtotal", header.Subtotal, ""},
		{"vendor_bill_discount_total", header.DiscountTotal, ""},
		{"vendor_bill_tax_total", header.TaxTotal, ""},
		{"vendor_bill_adjustment", in.Adjustment, ""},
		{"vendor_bill_grand_total", header.GrandTotal, ""},
		{"vendor_bill_balance_due", header.BalanceDue, ""},
		{"vendor_bill_custom_fields", custom, ""},
		{"vendor_bill_updated_by", nullableInt(actorEmployeeID), ""},
	}

	updateSQL, updateArgs := buildUpdateSet("vendor_bill", []any{uuid}, cv,
		[]string{"vendor_bill_updated_at = NOW()", "vendor_bill_record_version = vendor_bill_record_version + 1"},
		"vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL")
	if _, err = tx.Exec(ctx, updateSQL, updateArgs...); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms or currency) does not exist."}
		}
		return nil, fmt.Errorf("update vendor bill: %w", err)
	}

	// Preserve the purchase order lineage and keep what the bill claims as
	// billed in step with the edited quantities.
	if err := syncConversionOnEdit(ctx, tx, internalID, lines); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE vendor_bill_item SET item_deleted_at = NOW() WHERE vendor_bill_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous vendor bill items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := auditTx(ctx, tx, auditUpdate, uuid, before, duplicateExtra(dupUUID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update vendor bill: %w", err)
	}
	out, err := Get(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	out.DuplicateOverride = dupUUID != ""
	out.DuplicateOfUUID = dupUUID
	return out, nil
}

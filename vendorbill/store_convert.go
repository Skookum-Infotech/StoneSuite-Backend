// vendorbill/store_convert.go — AD-8: a Purchase Order converts into a
// Vendor Bill. Lives here (the destination package), not in purchaseorder/ --
// the same convention purchaseorder.ConvertFromRequisition, salesorder.
// ConvertFromQuote, and invoice.ConvertFromSalesOrder already established:
// the destination-owning package performs the write, raw SQL against the
// source table, no import of the purchaseorder Go package.
//
// Unlike ConvertFromRequisition, a purchase order may convert MORE THAN ONCE
// -- vendors routinely bill a single order in installments -- so there is no
// idempotent-replay short-circuit here; every call creates a new bill. Only
// a received purchase order (RCVD or CLSD) may convert.
package vendorbill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPurchaseOrderNotFound is returned when the source purchase order uuid
// matches no live row.
var ErrPurchaseOrderNotFound = errors.New("purchase order not found")

// purchaseOrderSnapshot is the subset of a source purchase order's header
// the convert path copies verbatim.
type purchaseOrderSnapshot struct {
	internalID                                  int
	statusCode                                  string
	vendorID                                    int
	vendorName                                  string
	referenceNumber                             string
	paymentTermsID                              *int
	currencyID                                  *int
	salesTaxPercent                             float64
	memo, notes, internalNotes, termsConditions string
	customFields                                map[string]any
}

// loadPurchaseOrderSnapshot loads a live purchase order's header snapshot by
// external uuid inside tx (row is not locked -- a source document does not
// change once converted, and purchase order has no FOR UPDATE convention
// elsewhere for this read, mirroring loadRequisitionSnapshot).
func loadPurchaseOrderSnapshot(ctx context.Context, tx pgx.Tx, poUUID string) (*purchaseOrderSnapshot, error) {
	var s purchaseOrderSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT po.purchase_order_id, rs.record_status_code,
		       po.purchase_order_vendor_id, po.purchase_order_vendor_name,
		       po.purchase_order_reference_number, po.purchase_order_payment_terms, po.purchase_order_currency,
		       po.purchase_order_sales_tax_percent,
		       po.purchase_order_memo, po.purchase_order_notes, po.purchase_order_internal_notes, po.purchase_order_terms_conditions,
		       po.purchase_order_custom_fields
		FROM purchase_order po JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL`, poUUID).Scan(
		&s.internalID, &s.statusCode,
		&s.vendorID, &s.vendorName,
		&s.referenceNumber, &s.paymentTermsID, &s.currencyID,
		&s.salesTaxPercent,
		&s.memo, &s.notes, &s.internalNotes, &s.termsConditions,
		&customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPurchaseOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load purchase order snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}

// poSourceLine is one live purchase_order_item row's frozen values, copied
// verbatim (not re-priced) into the new bill's lines.
type poSourceLine struct {
	uuid                string
	lineNumber          int
	inventoryItemID     *int
	itemName, sku, desc string
	unitID              *int
	unitCode            string
	quantity            float64
	unitPrice           float64
	discountPercent     float64
	taxRateID           *int
	taxPercent          float64
}

// loadPurchaseOrderSourceLines loads a live purchase order's lines by its
// internal id.
func loadPurchaseOrderSourceLines(ctx context.Context, tx pgx.Tx, poInternalID int) ([]poSourceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT purchase_order_item_uuid, line_number, inventory_item_id,
		       item_name, sku, description, unit_id, COALESCE(unit_code,''),
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent
		FROM purchase_order_item
		WHERE purchase_order_id = $1 AND item_deleted_at IS NULL
		ORDER BY line_number`, poInternalID)
	if err != nil {
		return nil, fmt.Errorf("load purchase order lines: %w", err)
	}
	defer rows.Close()
	out := []poSourceLine{}
	for rows.Next() {
		var l poSourceLine
		if err := rows.Scan(
			&l.uuid, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent,
		); err != nil {
			return nil, fmt.Errorf("scan purchase order line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "Purchase order has no line items to convert."}
	}
	return out, nil
}

// insertConvertedLines bulk-inserts PO-sourced lines as vendor_bill_item
// rows, copied verbatim (never re-priced -- AD-8), each linked back to its
// source purchase_order_item for traceability (AD-3). Returns the
// {poItemUuid: billItemUuid} lineage map and the computed line money (for
// the header recompute).
func insertConvertedLines(ctx context.Context, tx pgx.Tx, vbInternalID int, lines []poSourceLine, actorEmployeeID int) (map[string]string, []LineMoney, error) {
	lineMap := make(map[string]string, len(lines))
	lineMoneys := make([]LineMoney, 0, len(lines))
	for _, l := range lines {
		money := ComputeLine(CalcLineInput{
			Quantity: l.quantity, UnitPrice: l.unitPrice,
			DiscountPercent: l.discountPercent, TaxPercent: l.taxPercent,
		})
		var srcPOItemInternalID int
		if err := tx.QueryRow(ctx,
			`SELECT purchase_order_item_id FROM purchase_order_item WHERE purchase_order_item_uuid = $1`, l.uuid,
		).Scan(&srcPOItemInternalID); err != nil {
			return nil, nil, fmt.Errorf("resolve source purchase order item: %w", err)
		}
		var newLineUUID string
		err := tx.QueryRow(ctx, `
			INSERT INTO vendor_bill_item (
				vendor_bill_id, line_number, inventory_item_id, purchase_order_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)
			RETURNING vendor_bill_item_uuid`,
			vbInternalID, l.lineNumber, l.inventoryItemID, srcPOItemInternalID,
			l.itemName, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			money.Subtotal, money.Discount, money.Tax, money.Total,
			nullableInt(actorEmployeeID),
		).Scan(&newLineUUID)
		if err != nil {
			return nil, nil, fmt.Errorf("insert converted vendor bill item: %w", err)
		}
		lineMap[l.uuid] = newLineUUID
		lineMoneys = append(lineMoneys, money)
	}
	return lineMap, lineMoneys, nil
}

// ConvertFromPurchaseOrder creates a new VendorBill as a snapshot copy of a
// live purchase order's header + lines: every line item is copied verbatim
// (not re-priced against current catalog data), header totals are
// recomputed from the copied lines via vendorbill's own calc, and the
// lineage is recorded in vendor_bill_conversion. Only a received purchase
// order (RCVD or CLSD) may convert.
func ConvertFromPurchaseOrder(ctx context.Context, pool *pgxpool.Pool, poUUID string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin convert purchase order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadPurchaseOrderSnapshot(ctx, tx, poUUID)
	if err != nil {
		return nil, err
	}
	if src.statusCode != "RCVD" && src.statusCode != "CLSD" {
		return nil, ClientError{Msg: "Only a received purchase order can be converted to a vendor bill."}
	}

	lines, err := loadPurchaseOrderSourceLines(ctx, tx, src.internalID)
	if err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_bill_status", draftStatusID, ""},
		{"vendor_bill_vendor_id", src.vendorID, ""},
		{"vendor_bill_vendor_name", src.vendorName, ""},
		{"vendor_bill_purchase_order_id", src.internalID, ""},
		{"vendor_bill_reference_number", src.referenceNumber, ""},
		{"vendor_bill_date", "now", "::date"},
		{"vendor_bill_sales_tax_percent", src.salesTaxPercent, ""},
		{"vendor_bill_memo", src.memo, ""},
		{"vendor_bill_notes", src.notes, ""},
		{"vendor_bill_internal_notes", src.internalNotes, ""},
		{"vendor_bill_terms_conditions", src.termsConditions, ""},
		{"vendor_bill_owner_id", nullableInt(actorEmployeeID), ""},
		{"vendor_bill_payment_terms", src.paymentTermsID, ""},
		{"vendor_bill_currency", src.currencyID, ""},
		{"vendor_bill_custom_fields", src.customFields, ""},
		{"vendor_bill_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("vendor_bill", cv, "vendor_bill_id, vendor_bill_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms or currency) does not exist."}
		}
		return nil, fmt.Errorf("insert converted vendor bill: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE vendor_bill SET vendor_bill_number = $1 WHERE vendor_bill_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set vendor bill number: %w", err)
	}

	lineMap, lineMoneys, err := insertConvertedLines(ctx, tx, internalID, lines, actorEmployeeID)
	if err != nil {
		return nil, err
	}

	// Recompute and store header totals from the inserted lines (mirrors
	// Create -- the header row above was inserted with zero totals).
	// vendor_bill_balance_due starts equal to vendor_bill_grand_total: a
	// freshly converted bill is DRFT (not yet payable -- AD-7's
	// PayableStatuses gates APPV/PART/ODUE only), so amount_paid is 0 by
	// definition here.
	h := ComputeHeader(lineMoneys, 0, 0)
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET
			vendor_bill_subtotal = $2, vendor_bill_discount_total = $3,
			vendor_bill_tax_total = $4, vendor_bill_grand_total = $5, vendor_bill_balance_due = $5
		WHERE vendor_bill_id = $1`, internalID, h.Subtotal, h.DiscountTotal, h.TaxTotal, h.GrandTotal); err != nil {
		return nil, fmt.Errorf("set converted vendor bill totals: %w", err)
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	snapshot := make(map[string]any, len(lineMap))
	for poItemUUID, vbItemUUID := range lineMap {
		snapshot[poItemUUID] = vbItemUUID
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal conversion snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_conversion (purchase_order_id, vendor_bill_id, converted_by, snapshot)
		VALUES ($1, $2, $3, $4)`,
		src.internalID, internalID, nullableInt(actorEmployeeID), snapshotJSON); err != nil {
		return nil, fmt.Errorf("insert vendor_bill_conversion: %w", err)
	}

	// Mark the source purchase order's own history with the conversion.
	if _, err := tx.Exec(ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('vendorBillId', $3::int, 'vendorBillUuid', $4::text))`,
		src.internalID, nullableInt(actorEmployeeID), internalID, newUUID); err != nil {
		return nil, fmt.Errorf("insert purchase order convert history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit convert purchase order: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

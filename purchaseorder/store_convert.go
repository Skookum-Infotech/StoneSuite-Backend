// purchaseorder/store_convert.go — AD-8 of the Requisition module design
// (docs/superpowers/specs/2026-08-01-requisition-module-design.md): a
// Requisition converts into a Purchase Order. Lives here, not in
// requisition/, because the destination-owning package performs the write —
// the same convention salesorder.ConvertFromQuote and
// invoice.ConvertFromSalesOrder already established (raw SQL against the
// source table, no import of the source package).
package purchaseorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrRequisitionNotFound is returned when the source requisition uuid
// matches no live row.
var ErrRequisitionNotFound = errors.New("requisition not found")

// requisitionSnapshot is the subset of a source requisition's header the
// convert path copies verbatim.
type requisitionSnapshot struct {
	internalID      int
	statusCode      string
	memo            string
	paymentTermsID  *int
	salesTaxPercent float64
	customFields    map[string]any
}

// loadRequisitionSnapshot loads a live requisition's header snapshot by
// external uuid inside tx (row is not locked — a source document does not
// change once converted, and requisition has no FOR UPDATE convention
// elsewhere for this read).
func loadRequisitionSnapshot(ctx context.Context, tx pgx.Tx, requisitionUUID string) (*requisitionSnapshot, error) {
	var s requisitionSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT reqn.requisition_id, rs.record_status_code,
		       reqn.requisition_memo, reqn.requisition_payment_terms,
		       reqn.requisition_sales_tax_percent, reqn.requisition_custom_fields
		FROM requisition reqn JOIN lkp_record_status rs ON rs.record_status_id = reqn.requisition_status
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL`, requisitionUUID).Scan(
		&s.internalID, &s.statusCode,
		&s.memo, &s.paymentTermsID,
		&s.salesTaxPercent, &customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRequisitionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load requisition snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}

// requisitionSourceLine is one live requisition_item row's frozen values,
// copied verbatim (not re-priced) into the new PO's lines.
type requisitionSourceLine struct {
	uuid                string
	lineNumber          int
	inventoryItemID     *int
	itemName, sku, desc string
	unitID              *int
	unitCode            string
	quantity            float64
	unitPrice           float64
}

// loadRequisitionSourceLines loads a live requisition's lines by its internal id.
func loadRequisitionSourceLines(ctx context.Context, tx pgx.Tx, requisitionInternalID int) ([]requisitionSourceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT requisition_item_uuid, line_number, inventory_item_id,
		       item_name, sku, description, unit_id, COALESCE(unit_code,''),
		       quantity, estimated_unit_price
		FROM requisition_item
		WHERE requisition_id = $1 AND item_deleted_at IS NULL
		ORDER BY line_number`, requisitionInternalID)
	if err != nil {
		return nil, fmt.Errorf("load requisition lines: %w", err)
	}
	defer rows.Close()
	out := []requisitionSourceLine{}
	for rows.Next() {
		var l requisitionSourceLine
		if err := rows.Scan(
			&l.uuid, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice,
		); err != nil {
			return nil, fmt.Errorf("scan requisition line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "Requisition has no line items to convert."}
	}
	return out, nil
}

// insertConvertedLines bulk-inserts requisition-sourced lines as
// purchase_order_item rows. A converted line has no discount and no tax
// rate id of its own (the requisition module has no per-line tax/discount —
// AD-3 of the Requisition design), so each line's tax is computed from the
// requisition's flat header tax percent, matching how requisition itself
// derived its own totals.
func insertConvertedLines(ctx context.Context, tx pgx.Tx, poInternalID int, lines []requisitionSourceLine, headerTaxPercent float64, actorEmployeeID int) (map[string]string, error) {
	lineMap := make(map[string]string, len(lines))
	for _, l := range lines {
		money := ComputeLine(CalcLineInput{
			Quantity: l.quantity, UnitPrice: l.unitPrice,
			DiscountPercent: 0, TaxPercent: headerTaxPercent,
		})
		var newLineUUID string
		err := tx.QueryRow(ctx, `
			INSERT INTO purchase_order_item (
				purchase_order_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,0,$11, $12,$13,$14,$15, $16)
			RETURNING purchase_order_item_uuid`,
			poInternalID, l.lineNumber, l.inventoryItemID,
			l.itemName, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, headerTaxPercent,
			money.Subtotal, money.Discount, money.Tax, money.Total,
			nullableInt(actorEmployeeID),
		).Scan(&newLineUUID)
		if err != nil {
			return nil, fmt.Errorf("insert converted purchase order item: %w", err)
		}
		lineMap[l.uuid] = newLineUUID
	}
	return lineMap, nil
}

// ConvertFromRequisition creates a new PurchaseOrder as a snapshot copy of a
// live requisition's header + lines: every line item is copied verbatim
// (not re-priced against current catalog data), header totals are
// recomputed from the copied lines via purchaseorder's own calc, and the
// lineage is recorded in requisition_conversion (a requisition/purchase_order
// join row with a lightweight {requisitionItemUuid: purchaseOrderItemUuid}
// line-mapping snapshot).
//
// vendorUUID is required: a requisition's own suggested vendor (if any) is
// only ever a suggestion (AD-2 of the Requisition design) — the UI offers it
// as a pre-filled default, but the caller must always confirm a concrete
// vendor for the PO the store layer will not silently promote a suggestion
// to a commitment. Only an APPV (approved) requisition may convert.
//
// A requisition may only convert once (uq_requisition_conversion_requisition).
// Replaying the call after a successful conversion returns the existing
// purchase order and created=false instead of erroring, so the endpoint is
// safe to retry. Mirrors salesorder.ConvertFromQuote exactly.
func ConvertFromRequisition(ctx context.Context, pool *pgxpool.Pool, requisitionUUID, vendorUUID string, actorEmployeeID int) (po *PurchaseOrder, created bool, err error) {
	if strings.TrimSpace(vendorUUID) == "" {
		return nil, false, ClientError{Msg: "A vendor is required to convert a requisition to a purchase order."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin convert requisition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadRequisitionSnapshot(ctx, tx, requisitionUUID)
	if err != nil {
		return nil, false, err
	}

	// Idempotent replay check comes before the status gate: a requisition
	// that already converted must return its existing PO even if its status
	// later moved away from APPV, so a retried call never errors on a call
	// that already succeeded once.
	var existingUUID string
	err = tx.QueryRow(ctx, `
		SELECT po.purchase_order_uuid
		FROM requisition_conversion rc
		JOIN purchase_order po ON po.purchase_order_id = rc.purchase_order_id
		WHERE rc.requisition_id = $1`, src.internalID).Scan(&existingUUID)
	if err == nil {
		existing, gerr := Get(ctx, pool, existingUUID)
		if gerr != nil {
			return nil, false, gerr
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("check existing conversion: %w", err)
	}

	if src.statusCode != "APPV" {
		return nil, false, ClientError{Msg: "Only an approved requisition can be converted to a purchase order."}
	}

	vendorInternalID, vendorName, err := vendorSnapshot(ctx, tx, vendorUUID)
	if err != nil {
		return nil, false, err
	}

	lines, err := loadRequisitionSourceLines(ctx, tx, src.internalID)
	if err != nil {
		return nil, false, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, pordRecordTypeCode)
	if err != nil {
		return nil, false, fmt.Errorf("resolve PORD record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, false, fmt.Errorf("resolve DRFT status: %w", err)
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"purchase_order_status", draftStatusID, ""},
		{"purchase_order_vendor_id", vendorInternalID, ""},
		{"purchase_order_vendor_name", vendorName, ""},
		{"purchase_order_date", "now", "::date"},
		{"purchase_order_sales_tax_percent", src.salesTaxPercent, ""},
		{"purchase_order_memo", src.memo, ""},
		{"purchase_order_owner_id", nullableInt(actorEmployeeID), ""},
		{"purchase_order_payment_terms", src.paymentTermsID, ""},
		{"purchase_order_custom_fields", src.customFields, ""},
		{"purchase_order_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals(AddressInput{})...)

	insertSQL, insertArgs := buildInsert("purchase_order", cv, "purchase_order_id, purchase_order_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, false, ClientError{Msg: "One of the referenced ids (payment terms or vendor) does not exist."}
		}
		return nil, false, fmt.Errorf("insert converted purchase order: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE purchase_order SET purchase_order_number = $1 WHERE purchase_order_id = $2`, number, internalID); err != nil {
		return nil, false, fmt.Errorf("set purchase order number: %w", err)
	}

	lineMap, err := insertConvertedLines(ctx, tx, internalID, lines, src.salesTaxPercent, actorEmployeeID)
	if err != nil {
		return nil, false, err
	}

	// Recompute and store header totals from the inserted lines (mirrors
	// Create — the header row above was inserted with zero totals).
	lineMoney := make([]LineMoney, 0, len(lines))
	for _, l := range lines {
		lineMoney = append(lineMoney, ComputeLine(CalcLineInput{
			Quantity: l.quantity, UnitPrice: l.unitPrice, DiscountPercent: 0, TaxPercent: src.salesTaxPercent,
		}))
	}
	h := ComputeHeader(lineMoney, 0, 0)
	if _, err := tx.Exec(ctx, `
		UPDATE purchase_order SET
			purchase_order_subtotal = $2, purchase_order_discount_total = $3,
			purchase_order_tax_total = $4, purchase_order_grand_total = $5
		WHERE purchase_order_id = $1`, internalID, h.Subtotal, h.DiscountTotal, h.TaxTotal, h.GrandTotal); err != nil {
		return nil, false, fmt.Errorf("set converted purchase order totals: %w", err)
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	snapshot := make(map[string]any, len(lineMap))
	for reqnItemUUID, poItemUUID := range lineMap {
		snapshot[reqnItemUUID] = poItemUUID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO requisition_conversion (requisition_id, purchase_order_id, converted_by, snapshot)
		VALUES ($1, $2, $3, $4)`,
		src.internalID, internalID, nullableInt(actorEmployeeID), snapshot)
	if err != nil {
		if isUniqueViolation(err) {
			// Lost a concurrent-conversion race: fetch and return the winner.
			existing, _, gerr := ConvertFromRequisition(ctx, pool, requisitionUUID, vendorUUID, actorEmployeeID)
			return existing, false, gerr
		}
		return nil, false, fmt.Errorf("insert requisition_conversion: %w", err)
	}

	// Mark the source requisition's own history with the conversion (schema's
	// chk_reqn_history_action already allows 'convert').
	if _, err := tx.Exec(ctx, `
		INSERT INTO requisition_history (requisition_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('purchaseOrderId', $3::int, 'purchaseOrderUuid', $4::text))`,
		src.internalID, nullableInt(actorEmployeeID), internalID, newUUID); err != nil {
		return nil, false, fmt.Errorf("insert requisition convert history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit convert requisition: %w", err)
	}
	got, err := Get(ctx, pool, newUUID)
	if err != nil {
		return nil, false, err
	}
	return got, true, nil
}

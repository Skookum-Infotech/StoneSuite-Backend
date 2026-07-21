// salesorder/store_convert.go
package salesorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrQuoteNotFound is returned when the source quote uuid matches no live
// row.
var ErrQuoteNotFound = errors.New("quote not found")

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (code 23505) — the concurrent-conversion race on
// uq_quote_conversion_quote.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// quoteSnapshot is the subset of a source quote's header the convert path
// copies verbatim — including its already-frozen billing/shipping snapshot
// (spec AD-4: a snapshot is copied forward, never re-derived from the live
// customer, so later customer edits don't rewrite the chain).
type quoteSnapshot struct {
	internalID           int
	customerInternalID   int
	poNumber, refNumber  string
	memo, notes          string
	internalNotes, terms string
	paymentTermsID       *int
	priceLevelID         *int
	currencyID           *int
	salesRepEmployeeID   *int
	ownerEmployeeID      *int
	salesTaxPercent      float64
	shippingCharge       float64
	adjustment           float64
	shipSameAsBill       bool
	billing, shipping    AddressInput
	customFields         map[string]any
}

// loadQuoteSnapshot loads a live quote's header snapshot by external uuid
// inside tx.
func loadQuoteSnapshot(ctx context.Context, tx pgx.Tx, quoteUUID string) (*quoteSnapshot, error) {
	var s quoteSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT quote_id, quote_customer_id,
		       quote_po_number, quote_reference_number,
		       quote_memo, quote_notes, quote_internal_notes, quote_terms_conditions,
		       quote_payment_terms, quote_price_level, quote_currency,
		       quote_sales_rep_id, quote_owner_id, quote_sales_tax_percent,
		       quote_shipping_charge, quote_adjustment, quote_ship_same_as_bill,
		       quote_bill_customer_name, quote_bill_attention,
		       quote_bill_addr_line1, quote_bill_addr_line2, quote_bill_addr_suitenum,
		       quote_bill_addr_city, quote_bill_addr_state, quote_bill_addr_zip,
		       quote_bill_addr_country, quote_bill_phone, quote_bill_fax, quote_bill_email,
		       quote_ship_customer_name, quote_ship_attention,
		       quote_ship_addr_line1, quote_ship_addr_line2, quote_ship_addr_suitenum,
		       quote_ship_addr_city, quote_ship_addr_state, quote_ship_addr_zip,
		       quote_ship_addr_country, quote_ship_phone, quote_ship_fax, quote_ship_email,
		       quote_custom_fields
		FROM quote WHERE quote_uuid = $1 AND quote_deleted_at IS NULL`, quoteUUID).Scan(
		&s.internalID, &s.customerInternalID,
		&s.poNumber, &s.refNumber,
		&s.memo, &s.notes, &s.internalNotes, &s.terms,
		&s.paymentTermsID, &s.priceLevelID, &s.currencyID,
		&s.salesRepEmployeeID, &s.ownerEmployeeID, &s.salesTaxPercent,
		&s.shippingCharge, &s.adjustment, &s.shipSameAsBill,
		&s.billing.CustomerName, &s.billing.Attention,
		&s.billing.AddrLine1, &s.billing.AddrLine2, &s.billing.SuiteUnit,
		&s.billing.City, &s.billing.StateID, &s.billing.Zip,
		&s.billing.CountryID, &s.billing.Phone, &s.billing.Fax, &s.billing.Email,
		&s.shipping.CustomerName, &s.shipping.Attention,
		&s.shipping.AddrLine1, &s.shipping.AddrLine2, &s.shipping.SuiteUnit,
		&s.shipping.City, &s.shipping.StateID, &s.shipping.Zip,
		&s.shipping.CountryID, &s.shipping.Phone, &s.shipping.Fax, &s.shipping.Email,
		&customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrQuoteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}

// quoteSourceLine is one live quote_item row's frozen values, copied
// verbatim (not re-priced) into the new order's lines. uuid is kept for the
// quote_conversion.snapshot line-mapping audit trail.
type quoteSourceLine struct {
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
	money               LineMoney
}

// loadQuoteSourceLines loads a live quote's lines by its internal id.
func loadQuoteSourceLines(ctx context.Context, tx pgx.Tx, quoteInternalID int) ([]quoteSourceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT quote_item_uuid, line_number, inventory_item_id,
		       item_name, sku, description, unit_id, COALESCE(unit_code,''),
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
		       line_subtotal, line_discount, line_tax, line_total
		FROM quote_item
		WHERE quote_id = $1 AND item_deleted_at IS NULL
		ORDER BY line_number`, quoteInternalID)
	if err != nil {
		return nil, fmt.Errorf("load quote lines: %w", err)
	}
	defer rows.Close()
	out := []quoteSourceLine{}
	for rows.Next() {
		var l quoteSourceLine
		if err := rows.Scan(
			&l.uuid, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent,
			&l.money.Subtotal, &l.money.Discount, &l.money.Tax, &l.money.Total,
		); err != nil {
			return nil, fmt.Errorf("scan quote line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "Quote has no line items to convert."}
	}
	return out, nil
}

// insertConvertedLines bulk-inserts quote-sourced lines as sales_order_item
// rows, returning each new row's uuid keyed by the source quote_item's uuid
// (for the quote_conversion.snapshot line mapping — sales_order_item has no
// quote_item_id FK column by design; see schema.sql AD-6 comment).
func insertConvertedLines(ctx context.Context, tx pgx.Tx, orderInternalID int, lines []quoteSourceLine, actorEmployeeID int) (map[string]string, error) {
	lineMap := make(map[string]string, len(lines))
	for _, l := range lines {
		var newLineUUID string
		err := tx.QueryRow(ctx, `
			INSERT INTO sales_order_item (
				sales_order_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18)
			RETURNING sales_order_item_uuid`,
			orderInternalID, l.lineNumber, l.inventoryItemID,
			l.itemName, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		).Scan(&newLineUUID)
		if err != nil {
			return nil, fmt.Errorf("insert converted sales order item: %w", err)
		}
		lineMap[l.uuid] = newLineUUID
	}
	return lineMap, nil
}

// ConvertFromQuote creates a new Sales Order as a full snapshot copy of a
// live quote's header + lines (spec AD-6): every line item is copied
// verbatim (not re-priced against current catalog data), header totals are
// recomputed from the copied lines via salesorder's own calc, and the
// lineage is recorded in quote_conversion (a quote/sales_order join row with
// a lightweight {quoteItemUuid: salesOrderItemUuid} line-mapping snapshot —
// sales_order carries no quote FK column by design).
//
// A quote may only convert once (uq_quote_conversion_quote). Replaying the
// call after a successful conversion returns the existing order and
// created=false instead of erroring, so the endpoint is safe to retry.
func ConvertFromQuote(ctx context.Context, pool *pgxpool.Pool, quoteUUID string, actorEmployeeID int) (order *Order, created bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin convert quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadQuoteSnapshot(ctx, tx, quoteUUID)
	if err != nil {
		return nil, false, err
	}

	var existingUUID string
	err = tx.QueryRow(ctx, `
		SELECT so.sales_order_uuid
		FROM quote_conversion qc
		JOIN sales_order so ON so.sales_order_id = qc.sales_order_id
		WHERE qc.quote_id = $1`, src.internalID).Scan(&existingUUID)
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

	lines, err := loadQuoteSourceLines(ctx, tx, src.internalID)
	if err != nil {
		return nil, false, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, src.shippingCharge, src.adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, false, fmt.Errorf("resolve SORD record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, false, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if src.ownerEmployeeID != nil && *src.ownerEmployeeID > 0 {
		ownerEmployeeID = *src.ownerEmployeeID
	}

	dueDate, err := resolvePaymentDueDate(ctx, tx, "", "", src.paymentTermsID)
	if err != nil {
		return nil, false, err
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"sales_order_status", draftStatusID, ""},
		{"sales_order_customer_id", src.customerInternalID, ""},
		{"sales_order_po_number", src.poNumber, ""},
		{"sales_order_reference_number", src.refNumber, ""},
		{"sales_order_date", "now", "::date"},
		{"sales_order_payment_due_date", dueDate, "::date"},
		{"sales_order_sales_tax_percent", src.salesTaxPercent, ""},
		{"sales_order_memo", src.memo, ""},
		{"sales_order_notes", src.notes, ""},
		{"sales_order_internal_notes", src.internalNotes, ""},
		{"sales_order_terms_conditions", src.terms, ""},
		{"sales_order_sales_rep_id", src.salesRepEmployeeID, ""},
		{"sales_order_owner_id", nullableInt(ownerEmployeeID), ""},
		{"sales_order_payment_terms", src.paymentTermsID, ""},
		{"sales_order_price_level", src.priceLevelID, ""},
		{"sales_order_currency", src.currencyID, ""},
		{"sales_order_subtotal", header.Subtotal, ""},
		{"sales_order_discount_total", header.DiscountTotal, ""},
		{"sales_order_tax_total", header.TaxTotal, ""},
		{"sales_order_shipping_charge", src.shippingCharge, ""},
		{"sales_order_adjustment", src.adjustment, ""},
		{"sales_order_grand_total", header.GrandTotal, ""},
		{"sales_order_ship_same_as_bill", src.shipSameAsBill, ""},
		{"sales_order_custom_fields", src.customFields, ""},
		{"sales_order_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("sales_order_bill", src.billing)...)
	cv = append(cv, addrColVals("sales_order_ship", src.shipping)...)

	insertSQL, insertArgs := buildInsert("sales_order", cv, "sales_order_id, sales_order_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, false, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		if isCheckViolation(err) {
			return nil, false, ClientError{Msg: "One or more sales order values are out of range."}
		}
		return nil, false, fmt.Errorf("insert converted sales order: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE sales_order SET sales_order_number = $1 WHERE sales_order_id = $2`, number, internalID); err != nil {
		return nil, false, fmt.Errorf("set sales order number: %w", err)
	}

	lineMap, err := insertConvertedLines(ctx, tx, internalID, lines, actorEmployeeID)
	if err != nil {
		return nil, false, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	snapshot := make(map[string]any, len(lineMap))
	for quoteItemUUID, soItemUUID := range lineMap {
		snapshot[quoteItemUUID] = soItemUUID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO quote_conversion (quote_id, sales_order_id, converted_by, snapshot)
		VALUES ($1, $2, $3, $4)`,
		src.internalID, internalID, nullableInt(actorEmployeeID), snapshot)
	if err != nil {
		if isUniqueViolation(err) {
			// Lost a concurrent-conversion race: fetch and return the winner.
			existing, _, gerr := ConvertFromQuote(ctx, pool, quoteUUID, actorEmployeeID)
			return existing, false, gerr
		}
		return nil, false, fmt.Errorf("insert quote_conversion: %w", err)
	}

	// Mark the source quote's own history with the conversion (schema's
	// quote_history.action already allows 'convert').
	if _, err := tx.Exec(ctx, `
		INSERT INTO quote_history (quote_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('salesOrderId', $3::int, 'salesOrderUuid', $4::text))`,
		src.internalID, nullableInt(actorEmployeeID), internalID, newUUID); err != nil {
		return nil, false, fmt.Errorf("insert quote convert history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit convert quote: %w", err)
	}
	got, err := Get(ctx, pool, newUUID)
	if err != nil {
		return nil, false, err
	}
	return got, true, nil
}

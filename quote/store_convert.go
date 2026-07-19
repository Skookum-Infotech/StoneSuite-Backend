// quote/store_convert.go
package quote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrEstimateNotFound is returned when the source estimate uuid matches no
// live row.
var ErrEstimateNotFound = errors.New("estimate not found")

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (code 23505) — the concurrent-conversion race on
// uq_quote_estimate_once.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// estimateSnapshot is the subset of a source estimate's header the convert
// path copies verbatim — including its already-frozen billing/shipping
// snapshot (spec AD-4: a snapshot is copied forward, never re-derived from
// the live customer, so later customer edits don't rewrite the chain).
type estimateSnapshot struct {
	internalID                                  int
	customerInternalID                          int
	poNumber, referenceNumber                   string
	memo, notes, internalNotes, termsConditions string
	paymentTermsID, priceLevelID, currencyID    *int
	salesRepEmployeeID, ownerEmployeeID         *int
	salesTaxPercent, shippingCharge, adjustment float64
	shipSameAsBill                              bool
	billing, shipping                           AddressInput
	customFields                                map[string]any
}

// loadEstimateSnapshot loads a live estimate's header snapshot by external
// uuid inside tx (row is not locked — a source document does not change
// once converted, and estimate has no FOR UPDATE convention elsewhere).
func loadEstimateSnapshot(ctx context.Context, tx pgx.Tx, estimateUUID string) (*estimateSnapshot, error) {
	var s estimateSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT estimate_id, estimate_customer_id,
		       estimate_po_number, estimate_reference_number,
		       estimate_memo, estimate_notes, estimate_internal_notes, estimate_terms_conditions,
		       estimate_payment_terms, estimate_price_level, estimate_currency,
		       estimate_sales_rep_id, estimate_owner_id, estimate_sales_tax_percent,
		       estimate_shipping_charge, estimate_adjustment, estimate_ship_same_as_bill,
		       estimate_bill_customer_name, estimate_bill_attention,
		       estimate_bill_addr_line1, estimate_bill_addr_line2, estimate_bill_addr_suitenum,
		       estimate_bill_addr_city, estimate_bill_addr_state, estimate_bill_addr_zip,
		       estimate_bill_addr_country, estimate_bill_phone, estimate_bill_fax, estimate_bill_email,
		       estimate_ship_customer_name, estimate_ship_attention,
		       estimate_ship_addr_line1, estimate_ship_addr_line2, estimate_ship_addr_suitenum,
		       estimate_ship_addr_city, estimate_ship_addr_state, estimate_ship_addr_zip,
		       estimate_ship_addr_country, estimate_ship_phone, estimate_ship_fax, estimate_ship_email,
		       estimate_custom_fields
		FROM estimate WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL`, estimateUUID).Scan(
		&s.internalID, &s.customerInternalID,
		&s.poNumber, &s.referenceNumber,
		&s.memo, &s.notes, &s.internalNotes, &s.termsConditions,
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
		return nil, ErrEstimateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}

// estimateSourceLine is one live estimate_item row's frozen values, copied
// verbatim (not re-priced) into the new quote's lines.
type estimateSourceLine struct {
	internalID          int
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

// loadEstimateSourceLines loads a live estimate's lines by its internal id.
func loadEstimateSourceLines(ctx context.Context, tx pgx.Tx, estimateInternalID int) ([]estimateSourceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT estimate_item_id, line_number, inventory_item_id,
		       item_name, sku, description, unit_id, COALESCE(unit_code,''),
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
		       line_subtotal, line_discount, line_tax, line_total
		FROM estimate_item
		WHERE estimate_id = $1 AND item_deleted_at IS NULL
		ORDER BY line_number`, estimateInternalID)
	if err != nil {
		return nil, fmt.Errorf("load estimate lines: %w", err)
	}
	defer rows.Close()
	out := []estimateSourceLine{}
	for rows.Next() {
		var l estimateSourceLine
		if err := rows.Scan(
			&l.internalID, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent,
			&l.money.Subtotal, &l.money.Discount, &l.money.Tax, &l.money.Total,
		); err != nil {
			return nil, fmt.Errorf("scan estimate line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "Estimate has no line items to convert."}
	}
	return out, nil
}

// insertConvertedLines bulk-inserts estimate-sourced lines as quote_item
// rows, stamping estimate_item_id for per-line lineage (spec AD-6).
func insertConvertedLines(ctx context.Context, tx pgx.Tx, quoteInternalID int, lines []estimateSourceLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO quote_item (
				quote_id, line_number, inventory_item_id, estimate_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)`,
			quoteInternalID, l.lineNumber, l.inventoryItemID, l.internalID,
			l.itemName, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			return fmt.Errorf("insert converted quote item: %w", err)
		}
	}
	return nil
}

// ConvertFromEstimate creates a new Quote as a full snapshot copy of a live
// estimate's header + lines (spec AD-6): every line item is copied verbatim
// (not re-priced against current catalog data), header totals are
// recomputed from the copied lines via quote's own calc, and the new quote
// is linked back via quote_estimate_id. Conversion is orthogonal to the
// estimate's status (mirrors quote/transitions.go's note that a Quote's own
// "acceptance" is expressed by converting it, not by a status change).
//
// An estimate may only convert once (uq_quote_estimate_once). Replaying the
// call after a successful conversion returns the existing quote and
// created=false instead of erroring, so the endpoint is safe to retry.
func ConvertFromEstimate(ctx context.Context, pool *pgxpool.Pool, estimateUUID string, actorEmployeeID int) (q *Quote, created bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin convert estimate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadEstimateSnapshot(ctx, tx, estimateUUID)
	if err != nil {
		return nil, false, err
	}

	var existingUUID string
	err = tx.QueryRow(ctx,
		`SELECT quote_uuid FROM quote WHERE quote_estimate_id = $1 AND quote_deleted_at IS NULL`,
		src.internalID).Scan(&existingUUID)
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

	lines, err := loadEstimateSourceLines(ctx, tx, src.internalID)
	if err != nil {
		return nil, false, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, src.shippingCharge, src.adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, false, fmt.Errorf("resolve QUOT record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, false, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if src.ownerEmployeeID != nil && *src.ownerEmployeeID > 0 {
		ownerEmployeeID = *src.ownerEmployeeID
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"quote_status", draftStatusID, ""},
		{"quote_customer_id", src.customerInternalID, ""},
		{"quote_estimate_id", src.internalID, ""},
		{"quote_po_number", src.poNumber, ""},
		{"quote_reference_number", src.referenceNumber, ""},
		{"quote_date", "now", "::date"},
		{"quote_sales_tax_percent", src.salesTaxPercent, ""},
		{"quote_memo", src.memo, ""},
		{"quote_notes", src.notes, ""},
		{"quote_internal_notes", src.internalNotes, ""},
		{"quote_terms_conditions", src.termsConditions, ""},
		{"quote_sales_rep_id", src.salesRepEmployeeID, ""},
		{"quote_owner_id", nullableInt(ownerEmployeeID), ""},
		{"quote_payment_terms", src.paymentTermsID, ""},
		{"quote_price_level", src.priceLevelID, ""},
		{"quote_currency", src.currencyID, ""},
		{"quote_subtotal", header.Subtotal, ""},
		{"quote_discount_total", header.DiscountTotal, ""},
		{"quote_tax_total", header.TaxTotal, ""},
		{"quote_shipping_charge", src.shippingCharge, ""},
		{"quote_adjustment", src.adjustment, ""},
		{"quote_grand_total", header.GrandTotal, ""},
		{"quote_ship_same_as_bill", src.shipSameAsBill, ""},
		{"quote_custom_fields", src.customFields, ""},
		{"quote_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("quote_bill", src.billing)...)
	cv = append(cv, addrColVals("quote_ship", src.shipping)...)

	insertSQL, insertArgs := buildInsert("quote", cv, "quote_id, quote_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isUniqueViolation(err) {
			// Lost a concurrent-conversion race: fetch and return the winner.
			existing, _, gerr := ConvertFromEstimate(ctx, pool, estimateUUID, actorEmployeeID)
			return existing, false, gerr
		}
		if isForeignKeyViolation(err) {
			return nil, false, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, false, fmt.Errorf("insert converted quote: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE quote SET quote_number = $1 WHERE quote_id = $2`, number, internalID); err != nil {
		return nil, false, fmt.Errorf("set quote number: %w", err)
	}

	if err := insertConvertedLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, false, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	// Mark the source estimate's own history with the conversion (schema's
	// estimate_history.action already anticipates 'convert' for this).
	if _, err := tx.Exec(ctx, `
		INSERT INTO estimate_history (estimate_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('quoteId', $3::int, 'quoteUuid', $4::text))`,
		src.internalID, nullableInt(actorEmployeeID), internalID, newUUID); err != nil {
		return nil, false, fmt.Errorf("insert estimate convert history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit convert estimate: %w", err)
	}
	got, err := Get(ctx, pool, newUUID)
	if err != nil {
		return nil, false, err
	}
	return got, true, nil
}

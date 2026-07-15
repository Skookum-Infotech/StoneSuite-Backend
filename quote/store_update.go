// quote/store_update.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live quote's header fields and lines (recomputing
// totals) inside one transaction. Rejected once the quote has reached a
// terminal status (RJCT/EXPR/CANC) — a rejected, expired, or cancelled
// quote is immutable.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateQuoteInput, actorEmployeeID int) (*Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT est.quote_id, est.quote_customer_id, rs.record_status_code
		FROM quote est JOIN lkp_record_status rs ON rs.record_status_id = est.quote_status
		WHERE est.quote_uuid = $1 AND est.quote_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for update: %w", err)
	}
	if statusCode == "RJCT" || statusCode == "EXPR" || statusCode == "CANC" {
		return nil, ClientError{Msg: "A rejected, expired, or cancelled quote cannot be edited."}
	}

	_, custName, defBilling, defShipping, err := customerSnapshotByInternalID(ctx, tx, custInternalID)
	if err != nil {
		return nil, err
	}
	billing := overrideAddress(defBilling, in.Billing)
	var shipping AddressInput
	if in.ShipSameAsBilling {
		shipping = billing
	} else {
		shipping = overrideAddress(defShipping, in.Shipping)
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	cv := []colVal{
		{"quote_po_number", in.PONumber, ""},
		{"quote_reference_number", in.ReferenceNumber, ""},
		{"quote_date", orNow(in.QuoteDate), "::date"},
		{"quote_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"quote_sales_tax_percent", in.SalesTaxPercent, ""},
		{"quote_memo", in.Memo, ""},
		{"quote_notes", in.Notes, ""},
		{"quote_internal_notes", in.InternalNotes, ""},
		{"quote_terms_conditions", in.TermsConditions, ""},
		{"quote_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"quote_owner_id", in.OwnerEmployeeID, ""},
		{"quote_payment_terms", in.PaymentTermsID, ""},
		{"quote_price_level", in.PriceLevelID, ""},
		{"quote_currency", in.CurrencyID, ""},
		{"quote_subtotal", header.Subtotal, ""},
		{"quote_discount_total", header.DiscountTotal, ""},
		{"quote_tax_total", header.TaxTotal, ""},
		{"quote_shipping_charge", in.ShippingCharge, ""},
		{"quote_adjustment", in.Adjustment, ""},
		{"quote_grand_total", header.GrandTotal, ""},
		{"quote_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"quote_custom_fields", in.CustomFields, ""},
	}
	cv = append(cv, addrColVals("quote_bill", billing)...)
	cv = append(cv, addrColVals("quote_ship", shipping)...)
	cv = append(cv, colVal{"quote_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("quote", []any{uuid}, cv,
		[]string{"quote_updated_at = NOW()", "quote_record_version = quote_record_version + 1"},
		"quote_uuid = $1 AND quote_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("update quote: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE quote_item SET item_deleted_at = NOW() WHERE quote_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous quote items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update quote: %w", err)
	}
	_ = custName
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live quote deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE quote
		SET quote_deleted_at = NOW(), quote_deleted_by = $2
		WHERE quote_uuid = $1 AND quote_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete quote: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

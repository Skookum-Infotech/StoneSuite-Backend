// estimate/store_update.go
package estimate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live estimate's header fields and lines (recomputing
// totals) inside one transaction. Rejected once the estimate has reached a
// terminal status (RJCT/EXPR/CANC) — a rejected, expired, or cancelled
// estimate is immutable.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateEstimateInput, actorEmployeeID int) (*Estimate, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update estimate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT est.estimate_id, est.estimate_customer_id, rs.record_status_code
		FROM estimate est JOIN lkp_record_status rs ON rs.record_status_id = est.estimate_status
		WHERE est.estimate_uuid = $1 AND est.estimate_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate for update: %w", err)
	}
	if statusCode == "RJCT" || statusCode == "EXPR" || statusCode == "CANC" {
		return nil, ClientError{Msg: "A rejected, expired, or cancelled estimate cannot be edited."}
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

	// estimate_custom_fields is NOT NULL DEFAULT '{}'; a nil map encodes as SQL
	// NULL and violates the constraint.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"estimate_po_number", in.PONumber, ""},
		{"estimate_reference_number", in.ReferenceNumber, ""},
		{"estimate_date", orNow(in.EstimateDate), "::date"},
		{"estimate_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"estimate_sales_tax_percent", in.SalesTaxPercent, ""},
		{"estimate_memo", in.Memo, ""},
		{"estimate_notes", in.Notes, ""},
		{"estimate_internal_notes", in.InternalNotes, ""},
		{"estimate_terms_conditions", in.TermsConditions, ""},
		{"estimate_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"estimate_owner_id", in.OwnerEmployeeID, ""},
		{"estimate_payment_terms", in.PaymentTermsID, ""},
		{"estimate_price_level", in.PriceLevelID, ""},
		{"estimate_currency", in.CurrencyID, ""},
		{"estimate_subtotal", header.Subtotal, ""},
		{"estimate_discount_total", header.DiscountTotal, ""},
		{"estimate_tax_total", header.TaxTotal, ""},
		{"estimate_shipping_charge", in.ShippingCharge, ""},
		{"estimate_adjustment", in.Adjustment, ""},
		{"estimate_grand_total", header.GrandTotal, ""},
		{"estimate_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"estimate_custom_fields", custom, ""},
	}
	cv = append(cv, addrColVals("estimate_bill", billing)...)
	cv = append(cv, addrColVals("estimate_ship", shipping)...)
	cv = append(cv, colVal{"estimate_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("estimate", []any{uuid}, cv,
		[]string{"estimate_updated_at = NOW()", "estimate_record_version = estimate_record_version + 1"},
		"estimate_uuid = $1 AND estimate_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("update estimate: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE estimate_item SET item_deleted_at = NOW() WHERE estimate_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous estimate items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update estimate: %w", err)
	}
	_ = custName
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live estimate deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE estimate
		SET estimate_deleted_at = NOW(), estimate_deleted_by = $2
		WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete estimate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

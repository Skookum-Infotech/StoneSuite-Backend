// purchaseorder/store_update.go
package purchaseorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live purchase order's header fields and lines
// (recomputing totals) inside one transaction. Allowed only at DRFT (AD-10) —
// once submitted/approved/sent the document is in (or on its way to) the
// vendor's hands; recall it to draft (PAPV→DRFT / APPV→DRFT) to revise.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdatePurchaseOrderInput, actorEmployeeID int) (*PurchaseOrder, error) {
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update purchase order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT po.purchase_order_id, rs.record_status_code
		FROM purchase_order po JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL
		FOR UPDATE OF po`, uuid,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load purchase order for update: %w", err)
	}
	if statusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft purchase order can be edited. Recall it to draft first."}
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

	// purchase_order_custom_fields is NOT NULL DEFAULT '{}'; a nil map encodes
	// as SQL NULL and violates the constraint.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"purchase_order_reference_number", in.ReferenceNumber, ""},
		{"purchase_order_date", orNow(in.OrderDate), "::date"},
		{"purchase_order_expected_date", nullableDate(in.ExpectedDate), "::date"},
		{"purchase_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"purchase_order_memo", in.Memo, ""},
		{"purchase_order_notes", in.Notes, ""},
		{"purchase_order_internal_notes", in.InternalNotes, ""},
		{"purchase_order_terms_conditions", in.TermsConditions, ""},
		{"purchase_order_owner_id", in.OwnerEmployeeID, ""},
		{"purchase_order_payment_terms", in.PaymentTermsID, ""},
		{"purchase_order_currency", in.CurrencyID, ""},
		{"purchase_order_subtotal", header.Subtotal, ""},
		{"purchase_order_discount_total", header.DiscountTotal, ""},
		{"purchase_order_tax_total", header.TaxTotal, ""},
		{"purchase_order_shipping_charge", in.ShippingCharge, ""},
		{"purchase_order_adjustment", in.Adjustment, ""},
		{"purchase_order_grand_total", header.GrandTotal, ""},
		{"purchase_order_custom_fields", custom, ""},
	}
	cv = append(cv, addrColVals(in.ShipTo)...)
	cv = append(cv, colVal{"purchase_order_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("purchase_order", []any{uuid}, cv,
		[]string{"purchase_order_updated_at = NOW()", "purchase_order_record_version = purchase_order_record_version + 1"},
		"purchase_order_uuid = $1 AND purchase_order_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("update purchase order: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE purchase_order_item SET item_deleted_at = NOW() WHERE purchase_order_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous purchase order items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update purchase order: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live purchase order deleted. Only DRFT and CANC orders
// may be deleted (AD-9) — a document the vendor may hold keeps its trail.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE purchase_order po
		SET purchase_order_deleted_at = NOW(), purchase_order_deleted_by = $2
		FROM lkp_record_status rs
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL
		  AND rs.record_status_id = po.purchase_order_status
		  AND rs.record_status_code IN ('DRFT','CANC')`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete purchase order: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "not found" from "found but not deletable" for the 400/404 split.
		var exists bool
		if qerr := pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM purchase_order
				WHERE purchase_order_uuid = $1 AND purchase_order_deleted_at IS NULL)`, uuid).Scan(&exists); qerr == nil && exists {
			return ClientError{Msg: "Only a draft or cancelled purchase order can be deleted."}
		}
		return ErrNotFound
	}
	return nil
}

package salesorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ----- Update ---------------------------------------------------------------

// Update replaces a live order's header fields and lines (recomputing
// totals) inside one transaction. Rejected once the order has reached a
// terminal status (FILL/CANC) — a filled or cancelled order is immutable.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateOrderInput, actorEmployeeID int) (*Order, error) {
	// See Create: CustomFields is intentionally not validated against the
	// unrelated legacy "sales_order" v1 workflow.
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "Sales tax percent must be between 0 and 100."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT so.sales_order_id, so.sales_order_customer_id, rs.record_status_code
		FROM sales_order so JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order for update: %w", err)
	}
	if statusCode == "FILL" || statusCode == "CANC" {
		return nil, ClientError{Msg: "A filled or cancelled order cannot be edited."}
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

	dueDate, err := resolvePaymentDueDate(ctx, tx, in.OrderDate, in.PaymentDueDate, in.PaymentTermsID)
	if err != nil {
		return nil, err
	}

	// sales_order_custom_fields is NOT NULL DEFAULT '{}'; a nil map encodes as
	// SQL NULL and violates the constraint.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"sales_order_po_number", in.PONumber, ""},
		{"sales_order_reference_number", in.ReferenceNumber, ""},
		{"sales_order_date", orNow(in.OrderDate), "::date"},
		{"sales_order_expected_delivery", nullableDate(in.ExpectedDelivery), "::date"},
		{"sales_order_payment_due_date", dueDate, "::date"},
		{"sales_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"sales_order_memo", in.Memo, ""},
		{"sales_order_notes", in.Notes, ""},
		{"sales_order_internal_notes", in.InternalNotes, ""},
		{"sales_order_terms_conditions", in.TermsConditions, ""},
		{"sales_order_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"sales_order_owner_id", in.OwnerEmployeeID, ""},
		{"sales_order_payment_terms", in.PaymentTermsID, ""},
		{"sales_order_price_level", in.PriceLevelID, ""},
		{"sales_order_currency", in.CurrencyID, ""},
		{"sales_order_subtotal", header.Subtotal, ""},
		{"sales_order_discount_total", header.DiscountTotal, ""},
		{"sales_order_tax_total", header.TaxTotal, ""},
		{"sales_order_shipping_charge", in.ShippingCharge, ""},
		{"sales_order_adjustment", in.Adjustment, ""},
		{"sales_order_grand_total", header.GrandTotal, ""},
		{"sales_order_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"sales_order_custom_fields", custom, ""},
	}
	cv = append(cv, addrColVals("sales_order_bill", billing)...)
	cv = append(cv, addrColVals("sales_order_ship", shipping)...)
	cv = append(cv, colVal{"sales_order_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("sales_order", []any{uuid}, cv,
		[]string{"sales_order_updated_at = NOW()", "sales_order_record_version = sales_order_record_version + 1"},
		"sales_order_uuid = $1 AND sales_order_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more sales order values are out of range."}
		}
		return nil, fmt.Errorf("update sales order: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sales_order_item SET item_deleted_at = NOW() WHERE sales_order_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous sales order items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update sales order: %w", err)
	}
	_ = custName
	return Get(ctx, pool, uuid)
}

// customerSnapshotByInternalID is customerSnapshot keyed by the internal id
// already resolved on the order row (Update doesn't re-resolve by uuid).
func customerSnapshotByInternalID(ctx context.Context, q workflow.Querier, custInternalID int) (id int, name string, billing, shipping AddressInput, err error) {
	var uuid string
	if err := q.QueryRow(ctx, `SELECT customer_uuid FROM customer WHERE customer_id = $1`, custInternalID).Scan(&uuid); err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("resolve customer uuid: %w", err)
	}
	return customerSnapshot(ctx, q, uuid)
}

// ----- SoftDelete ------------------------------------------------------------

// SoftDelete marks a live order deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE sales_order
		SET sales_order_deleted_at = NOW(), sales_order_deleted_by = $2
		WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete sales order: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

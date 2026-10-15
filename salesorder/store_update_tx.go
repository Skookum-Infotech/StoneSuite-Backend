package salesorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DraftStatusCode is the status code of a Draft order. It is exported so a
// caller that edits on someone's behalf (the customer portal) can ask whether
// the order is still one staff have not moved on.
const DraftStatusCode = draftStatusCode

// UpdateOptions tunes UpdateInTx for a caller that is not the staff form.
type UpdateOptions struct {
	// ShippingAsGiven uses the input's Shipping block as it is instead of layering
	// it over the customer's own shipping address. For an order whose ship-to is a
	// job site, where an inherited suite number or state would be wrong. The same
	// switch CreateOptions has.
	ShippingAsGiven bool
}

// EditSnapshot is an order as an edit starts from it.
type EditSnapshot struct {
	StatusCode string
	// Input holds every header field and every live line, in the shape Update
	// takes, so that saving it back unchanged leaves the order exactly as it is.
	Input UpdateOrderInput
}

// editLineSelect reads the line columns an Update needs to round-trip, which the
// API's Line does not carry (warehouse, tax rate).
const editLineSelect = `
	SELECT soi.line_number, COALESCE(ii.inventory_item_uuid::text, ''),
	       COALESCE(soi.description, ''), COALESCE(soi.item_name, ''), COALESCE(soi.sku, ''),
	       COALESCE(soi.unit_code, ''), soi.quantity, soi.unit_price, soi.discount_percent,
	       soi.tax_rate_id, soi.tax_percent, soi.warehouse_id,
	       COALESCE(soi.kit_uuid::text, ''), COALESCE(soi.kit_name, ''),
	       soi.dim_length, soi.dim_width, soi.slabs, COALESCE(soi.dim_unit, '')
	FROM sales_order_item soi
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = soi.inventory_item_id
	WHERE soi.sales_order_id = $1 AND soi.item_deleted_at IS NULL
	ORDER BY soi.line_number`

// LoadForEdit locks the order's row for the rest of the caller's transaction and
// returns its current values, so a caller can decide whether it may be edited,
// change some of it and save it back with UpdateInTx without a staff save
// slipping in between. ErrNotFound when the order does not exist or was deleted.
func LoadForEdit(ctx context.Context, tx pgx.Tx, uuid string) (*EditSnapshot, error) {
	o, _, err := scanOrder(tx.QueryRow(ctx, orderSelect+`
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL
		FOR UPDATE OF so`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order for edit: %w", err)
	}
	var internalID int
	if err := tx.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1`, uuid).Scan(&internalID); err != nil {
		return nil, fmt.Errorf("resolve sales order id: %w", err)
	}
	items, err := loadEditLines(ctx, tx, internalID)
	if err != nil {
		return nil, err
	}

	snap := &EditSnapshot{StatusCode: o.StatusCode}
	in := &snap.Input
	in.PONumber, in.ReferenceNumber = o.PONumber, o.ReferenceNumber
	in.OrderDate, in.ExpectedDelivery, in.PaymentDueDate = o.OrderDate, o.ExpectedDelivery, o.PaymentDueDate
	in.InstallDate = o.InstallDate
	in.PaymentTermsID, in.PriceLevelID, in.CurrencyID = o.PaymentTermsID, o.PriceLevelID, o.CurrencyID
	in.SalesRepEmployeeID, in.OwnerEmployeeID = o.SalesRepEmployeeID, o.OwnerEmployeeID
	in.SalesTaxPercent = o.SalesTaxPercent
	in.Memo, in.Notes, in.InternalNotes, in.TermsConditions = o.Memo, o.Notes, o.InternalNotes, o.TermsConditions
	in.ShipSameAsBilling = o.ShipSameAsBilling
	in.Billing, in.Shipping = o.Billing, o.Shipping
	in.ShippingCharge, in.Adjustment = o.ShippingCharge, o.Adjustment
	in.CustomFields = o.CustomFields
	in.Items = items
	return snap, nil
}

// loadEditLines reads an order's live lines as Update input.
func loadEditLines(ctx context.Context, tx pgx.Tx, orderInternalID int) ([]LineInput2, error) {
	rows, err := tx.Query(ctx, editLineSelect, orderInternalID)
	if err != nil {
		return nil, fmt.Errorf("load sales order lines for edit: %w", err)
	}
	defer rows.Close()
	out := []LineInput2{}
	for rows.Next() {
		var l LineInput2
		var taxPercent float64
		if err := rows.Scan(&l.LineNumber, &l.InventoryItemUUID,
			&l.Description, &l.ItemName, &l.SKU, &l.UnitCode,
			&l.Quantity, &l.UnitPrice, &l.DiscountPercent,
			&l.TaxRateID, &taxPercent, &l.WarehouseID,
			&l.KitUUID, &l.KitName,
			&l.Length, &l.Width, &l.Slabs, &l.DimensionUnit); err != nil {
			return nil, fmt.Errorf("scan sales order line for edit: %w", err)
		}
		l.TaxPercent = &taxPercent
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load sales order lines for edit: %w", err)
	}
	return out, nil
}

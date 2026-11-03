// purchaseorder/on_order.go — the slabs a purchase order puts on order.
//
// The moment an order is sent to its vendor, every slab-tracked line that states
// the size of one slab (length and width) gets one inventory_slab per ordered
// slab, status 'on_order'. They are not stock -- see package inventory -- but
// they are visible in Inventory and are what the item receipt lists to be ticked
// off as they arrive. Lines are only editable while the order is a draft, so the
// slabs minted here can never disagree with the line they came from.
package purchaseorder

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
	"stonesuite-backend/linedim"
)

const (
	cancelledStatusCode = "CANC"
	closedStatusCode    = "CLSD"

	// slabsPerLineDefault is the slab count of a sized line that states none: its
	// quantity was derived as one slab (see linedim.Resolve).
	slabsPerLineDefault = 1
)

// fromInventoryErr re-wraps the inventory package's client errors as this
// package's, so the controller keeps mapping them to 400 instead of 500.
func fromInventoryErr(err error) error {
	if err != nil && inventory.IsClientError(err) {
		return ClientError{Msg: err.Error()}
	}
	return err
}

// onOrderLine is a purchase-order line that may need slabs minted.
type onOrderLine struct {
	id            int
	itemID        int
	length, width float64
	unit          string
	thickness     *float64 // the line's own, in `unit`
	itemThickness float64  // the item's, already millimetres; 0 = not stated
	slabs         int
	hasSlabs      bool // slabs were already minted for this line
}

// loadOnOrderLines returns the order's sized, catalogue-backed lines.
func loadOnOrderLines(ctx context.Context, tx pgx.Tx, poInternalID int) ([]onOrderLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT poi.purchase_order_item_id, poi.inventory_item_id,
		       poi.dim_length, poi.dim_width, poi.dim_unit, poi.dim_thickness,
		       COALESCE(poi.expected_slabs, $2), ii.inventory_item_thickness_mm,
		       EXISTS (SELECT 1 FROM inventory_slab s WHERE s.purchase_order_item_id = poi.purchase_order_item_id)
		FROM purchase_order_item poi
		JOIN inventory_item ii ON ii.inventory_item_id = poi.inventory_item_id
		WHERE poi.purchase_order_id = $1 AND poi.item_deleted_at IS NULL
		  AND poi.dim_length IS NOT NULL AND poi.dim_width IS NOT NULL
		ORDER BY poi.line_number`, poInternalID, slabsPerLineDefault)
	if err != nil {
		return nil, fmt.Errorf("load purchase order lines for on-order slabs: %w", err)
	}
	defer rows.Close()
	var out []onOrderLine
	for rows.Next() {
		var l onOrderLine
		if err := rows.Scan(&l.id, &l.itemID, &l.length, &l.width, &l.unit, &l.thickness,
			&l.slabs, &l.itemThickness, &l.hasSlabs); err != nil {
			return nil, fmt.Errorf("scan purchase order line for on-order slabs: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// thicknessMM is the thickness an On Order slab of this line is minted with: the
// line's own, else the item's, else 0 (the receipt then has to supply it).
func (l onOrderLine) thicknessMM() (float64, error) {
	if l.thickness != nil {
		mm, ok := linedim.ToMM(*l.thickness, l.unit)
		if !ok {
			return 0, ClientError{Msg: fmt.Sprintf("A line has an unknown dimension unit %q.", l.unit)}
		}
		return mm, nil
	}
	return l.itemThickness, nil
}

// allocateOnOrderSlabs mints the On Order slabs for an order that is being sent,
// inside the transition's transaction. It is idempotent: a line that already has
// slabs is left alone, so re-sending cannot double them. A line that is not a
// slab line, or does not state a slab size, mints nothing and keeps the manual
// slab entry on the item receipt.
func allocateOnOrderSlabs(ctx context.Context, tx pgx.Tx, poInternalID, actorEmployeeID int) error {
	lines, err := loadOnOrderLines(ctx, tx, poInternalID)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}

	var poNumber string
	var vendorID int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(purchase_order_number,''), purchase_order_vendor_id
		FROM purchase_order WHERE purchase_order_id = $1`, poInternalID).Scan(&poNumber, &vendorID); err != nil {
		return fmt.Errorf("load purchase order for on-order slabs: %w", err)
	}

	var (
		prefix      string
		next        int
		warehouseID int
	)
	for _, l := range lines {
		if l.hasSlabs {
			continue
		}
		item, err := inventory.ResolveItemUnitByID(ctx, tx, l.itemID)
		if err != nil {
			return fromInventoryErr(err)
		}
		if !item.IsSerialized() || item.UnitCategory != inventory.UnitCategoryArea {
			continue
		}
		lengthMM, okLength := linedim.ToMM(l.length, l.unit)
		widthMM, okWidth := linedim.ToMM(l.width, l.unit)
		if !okLength || !okWidth {
			return ClientError{Msg: fmt.Sprintf("A line has an unknown dimension unit %q.", l.unit)}
		}
		thicknessMM, err := l.thicknessMM()
		if err != nil {
			return err
		}

		// Looked up once, and only when there is something to mint, so an order
		// with no slab lines never needs a serial prefix or a location.
		if prefix == "" {
			if prefix, err = inventory.SlabSerialPrefix(poNumber); err != nil {
				return fromInventoryErr(err)
			}
			if next, err = inventory.NextSlabNumber(ctx, tx, prefix); err != nil {
				return err
			}
			if warehouseID, err = inventory.DefaultWarehouseID(ctx, tx); err != nil {
				return fromInventoryErr(err)
			}
		}
		vendor := vendorID
		if _, err := inventory.CreateOnOrderUnitsTx(ctx, tx, inventory.OnOrderParams{
			PurchaseOrderItemID: l.id,
			PONumber:            poNumber,
			Item:                item,
			WarehouseID:         warehouseID,
			VendorID:            &vendor,
			LengthMM:            lengthMM,
			WidthMM:             widthMM,
			ThicknessMM:         thicknessMM,
			Count:               l.slabs,
			SerialPrefix:        prefix,
			FirstNumber:         next,
		}, actorEmployeeID); err != nil {
			return fromInventoryErr(err)
		}
		next += l.slabs
	}
	return nil
}

// releaseOnOrderSlabs writes off the order's slabs that never arrived, because
// the order was cancelled or short-closed.
func releaseOnOrderSlabs(ctx context.Context, tx pgx.Tx, poInternalID int, toStatusCode string, actorEmployeeID int) error {
	reason := "Purchase order cancelled before this slab arrived."
	if toStatusCode == closedStatusCode {
		reason = "Purchase order closed before this slab arrived."
	}
	if _, err := inventory.ScrapOnOrderForPOTx(ctx, tx, poInternalID, reason, actorEmployeeID); err != nil {
		return fromInventoryErr(err)
	}
	return nil
}

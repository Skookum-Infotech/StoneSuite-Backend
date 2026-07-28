package inventoryadjustment

// store_lines.go — validating and writing the line set.
//
// Create and Update share this: an update replaces the whole line set rather
// than diffing it, because a draft is edited as a form and a partial diff would
// need every line to carry a stable client-side id it does not have.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
)

// resolvedLine is a validated line ready to insert.
type resolvedLine struct {
	itemID   int
	slabID   *int
	reasonID int
	itemName string
	sku      string
	unitID   int
	unitCode string
	serial   string
	qtyDelta float64
	notes    string
}

// resolveLines validates every line against the catalogue and the yard.
//
// A serialized line's MAGNITUDE is the slab's own area; only the SIGN comes
// from the caller. Trusting a client quantity for a slab is how a SQM
// measurement gets posted against a SQFT item — off by 10.76x, with no
// constraint anywhere that would catch it.
func resolveLines(ctx context.Context, q pgxQuerier, warehouseID int, in []LineInput) ([]resolvedLine, error) {
	if len(in) == 0 {
		return nil, ClientError{Msg: "An adjustment needs at least one line."}
	}
	out := make([]resolvedLine, 0, len(in))
	for i, li := range in {
		if strings.TrimSpace(li.InventoryItemID) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d needs an inventory item.", i+1)}
		}
		if li.ReasonID <= 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d needs a reason code.", i+1)}
		}
		item, err := inventory.ResolveItemForDocument(ctx, q, li.InventoryItemID)
		if err != nil {
			return nil, err
		}

		r := resolvedLine{
			itemID:   item.ID,
			reasonID: li.ReasonID,
			itemName: item.Name,
			sku:      item.SKU,
			unitID:   item.UnitID,
			unitCode: item.UnitCode,
			notes:    li.Notes,
		}

		if li.InventoryUnitID == nil || strings.TrimSpace(*li.InventoryUnitID) == "" {
			// Bulk line.
			if item.Tracking == trackingSerialized {
				return nil, ClientError{Msg: fmt.Sprintf(
					"Line %d: %s is tracked as individual units, so name the unit rather than a quantity.", i+1, item.Name)}
			}
			if li.QtyDelta == 0 {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d needs a non-zero quantity.", i+1)}
			}
			r.qtyDelta = li.QtyDelta
			out = append(out, r)
			continue
		}

		// Serialized line.
		if item.Tracking != trackingSerialized {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: %s is tracked by quantity, so give a quantity rather than a unit.", i+1, item.Name)}
		}
		unit, err := inventory.ResolveUnitForDocument(ctx, q, *li.InventoryUnitID, false)
		if err != nil {
			if err == inventory.ErrNotFound {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d names a unit that does not exist.", i+1)}
			}
			return nil, err
		}
		if unit.ItemID != item.ID {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: unit %s does not belong to %s.", i+1, unit.Serial, item.Name)}
		}
		if unit.WarehouseID != warehouseID {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: unit %s is in another warehouse. Adjust it there, or transfer it first.", i+1, unit.Serial)}
		}
		if li.QtyDelta == 0 {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: send a negative quantity to write unit %s off, or a positive one to bring it back.", i+1, unit.Serial)}
		}
		r.slabID = &unit.ID
		r.serial = unit.Serial
		// Sign from the caller, magnitude from the slab.
		r.qtyDelta = math.Copysign(unit.Area, li.QtyDelta)
		out = append(out, r)
	}
	return out, nil
}

// trackingSerialized mirrors inventory_item_tracking's serialized value.
const trackingSerialized = "serialized"

// replaceLines soft-deletes the existing line set and writes the new one.
//
// Soft delete rather than DELETE so a line that has already been referenced
// anywhere keeps its row, and so the partial unique indexes (which all carry
// line_deleted_at IS NULL) free up the slab for a corrected line.
func replaceLines(ctx context.Context, tx pgx.Tx, adjustmentID int, lines []resolvedLine, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_adjustment_line SET line_deleted_at = NOW()
		WHERE inventory_adjustment_id = $1 AND line_deleted_at IS NULL`, adjustmentID); err != nil {
		return fmt.Errorf("clear adjustment lines: %w", err)
	}
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory_adjustment_line (
				inventory_adjustment_id, line_number, inventory_item_id, inventory_slab_id,
				inventory_reason_id, item_name, sku, unit_id, unit_code, slab_serial,
				qty_delta, line_notes, line_created_by
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			adjustmentID, i+1, l.itemID, l.slabID, l.reasonID,
			l.itemName, l.sku, nullableInt(l.unitID), l.unitCode, l.serial,
			l.qtyDelta, l.notes, nullableInt(actorEmployeeID)); err != nil {
			return mapWriteErr(err, "insert line on")
		}
	}
	return nil
}

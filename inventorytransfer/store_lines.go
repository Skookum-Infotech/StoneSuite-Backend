package inventorytransfer

// store_lines.go — validating and writing the line set.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
)

const trackingSerialized = "serialized"

type resolvedLine struct {
	itemID   int
	slabID   *int
	itemName string
	sku      string
	unitID   int
	unitCode string
	serial   string
	qty      float64
	notes    string
}

// resolveLines validates every line against the catalogue and the source yard.
//
// Validation happens at DRAFT time for a good error message, and again under a
// row lock at SHIP time — see shipLine. A slab validated a week ago may have
// been cut, scrapped or transferred since, so the draft-time check is a
// convenience, never the guarantee.
func resolveLines(ctx context.Context, q pgxQuerier, fromWarehouseID int, in []LineInput) ([]resolvedLine, error) {
	if len(in) == 0 {
		return nil, ClientError{Msg: "A transfer needs at least one line."}
	}
	out := make([]resolvedLine, 0, len(in))
	for i, li := range in {
		if strings.TrimSpace(li.InventoryItemID) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d needs an inventory item.", i+1)}
		}
		item, err := inventory.ResolveItemForDocument(ctx, q, li.InventoryItemID)
		if err != nil {
			return nil, err
		}
		r := resolvedLine{
			itemID: item.ID, itemName: item.Name, sku: item.SKU,
			unitID: item.UnitID, unitCode: item.UnitCode, notes: li.Notes,
		}

		if li.InventoryUnitID == nil || strings.TrimSpace(*li.InventoryUnitID) == "" {
			if item.Tracking == trackingSerialized {
				return nil, ClientError{Msg: fmt.Sprintf(
					"Line %d: %s is tracked as individual units, so name the unit to move.", i+1, item.Name)}
			}
			if li.Qty <= 0 {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d needs a positive quantity.", i+1)}
			}
			r.qty = li.Qty
			out = append(out, r)
			continue
		}

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
		if unit.WarehouseID != fromWarehouseID {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: unit %s is not in the source warehouse.", i+1, unit.Serial)}
		}
		if unit.Status != inventory.StatusAvailable {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: unit %s is %s and cannot be transferred.", i+1, unit.Serial, unit.Status)}
		}
		r.slabID = &unit.ID
		r.serial = unit.Serial
		// A slab moves whole or not at all, so the quantity is its own area.
		r.qty = unit.Area
		out = append(out, r)
	}
	return out, nil
}

// replaceLines soft-deletes the existing line set and writes the new one.
func replaceLines(ctx context.Context, tx pgx.Tx, transferID int, lines []resolvedLine, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_transfer_line SET line_deleted_at = NOW()
		WHERE inventory_transfer_id = $1 AND line_deleted_at IS NULL`, transferID); err != nil {
		return fmt.Errorf("clear transfer lines: %w", err)
	}
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory_transfer_line (
				inventory_transfer_id, line_number, inventory_item_id, inventory_slab_id,
				item_name, sku, unit_id, unit_code, slab_serial, qty, line_notes, line_created_by
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			transferID, i+1, l.itemID, l.slabID,
			l.itemName, l.sku, nullableInt(l.unitID), l.unitCode, l.serial,
			l.qty, l.notes, nullableInt(actorEmployeeID)); err != nil {
			return mapWriteErr(err, "insert line on")
		}
	}
	return nil
}

// postableLine is one line's shape at ship and receive time.
type postableLine struct {
	id       int
	itemID   int
	slabUUID *string
	qty      float64
	serial   string
}

func postableLines(ctx context.Context, tx pgx.Tx, transferID int) ([]postableLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.inventory_transfer_line_id, l.inventory_item_id, s.inventory_slab_uuid,
		       l.qty, l.slab_serial
		FROM inventory_transfer_line l
		LEFT JOIN inventory_slab s ON s.inventory_slab_id = l.inventory_slab_id
		WHERE l.inventory_transfer_id = $1 AND l.line_deleted_at IS NULL
		ORDER BY l.line_number`, transferID)
	if err != nil {
		return nil, fmt.Errorf("load transfer lines: %w", err)
	}
	defer rows.Close()
	var out []postableLine
	for rows.Next() {
		var l postableLine
		if err := rows.Scan(&l.id, &l.itemID, &l.slabUUID, &l.qty, &l.serial); err != nil {
			return nil, fmt.Errorf("scan transfer line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

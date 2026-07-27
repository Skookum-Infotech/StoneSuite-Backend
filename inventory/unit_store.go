package inventory

// unit_store.go — shared unit helpers and the single-row read.
// Moved here from fabrication/slab_store.go: a physical piece of stock is an
// inventory concern, and leaving its CRUD inside the fabrication module meant
// the yard could only be managed through a fabrication job.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// unitSelect is the canonical unit projection, joined to everything a yard
// screen needs to render a row without a second round trip.
const unitSelect = `
	SELECT s.inventory_slab_uuid, s.slab_serial, s.slab_unit_kind,
	       s.slab_vendor_id, s.slab_supplier_code, s.slab_barcode,
	       ii.inventory_item_uuid, ii.inventory_item_name, ii.inventory_item_sku,
	       s.warehouse_id, w.warehouse_name,
	       b.inventory_bin_uuid, COALESCE(b.bin_path,''),
	       s.slab_bundle_id, bu.inventory_bundle_uuid, s.slab_block_id, s.slab_lot,
	       s.slab_length_mm, s.slab_width_mm, s.slab_thickness_mm, s.slab_area, s.slab_area_unit_id,
	       s.slab_form, s.slab_status, p.inventory_slab_uuid, r.inventory_slab_uuid,
	       s.slab_is_usable_remnant, s.slab_grade, s.slab_finish, s.slab_finish_id, s.slab_photo_key,
	       s.slab_created_at, s.slab_updated_at
	FROM inventory_slab s
	JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
	JOIN lkp_warehouse w   ON w.warehouse_id = s.warehouse_id
	LEFT JOIN inventory_bin b     ON b.inventory_bin_id = s.inventory_bin_id
	LEFT JOIN inventory_bundle bu ON bu.inventory_bundle_id = s.inventory_bundle_id
	LEFT JOIN inventory_slab p    ON p.inventory_slab_id = s.slab_parent_slab_id
	LEFT JOIN inventory_slab r    ON r.inventory_slab_id = s.slab_root_slab_id`

func scanUnit(row pgx.Row) (*Unit, error) {
	var u Unit
	if err := row.Scan(
		&u.ID, &u.Serial, &u.Kind,
		&u.VendorID, &u.SupplierCode, &u.Barcode,
		&u.InventoryItemID, &u.InventoryItemName, &u.InventoryItemSKU,
		&u.WarehouseID, &u.WarehouseName,
		&u.BinID, &u.BinPath,
		&u.BundleID, &u.BundleUUID, &u.BlockID, &u.Lot,
		&u.LengthMM, &u.WidthMM, &u.ThicknessMM, &u.Area, &u.AreaUnitID,
		&u.Form, &u.Status, &u.ParentUnitID, &u.RootUnitID,
		&u.IsUsableRemnant, &u.Grade, &u.Finish, &u.FinishID, &u.PhotoKey,
		&u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUnit loads one live unit by uuid.
func GetUnit(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Unit, error) {
	u, err := scanUnit(pool.QueryRow(ctx, unitSelect+`
		WHERE s.inventory_slab_uuid = $1 AND s.slab_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory unit: %w", err)
	}
	return u, nil
}

// unitRow is a unit's internal identity, resolved inside a transaction.
type unitRow struct {
	id          int
	itemID      int
	warehouseID int
	binID       *int
	bundleID    *int
	status      string
	area        float64
}

// unitByUUID resolves and locks a unit for update.
func unitByUUID(ctx context.Context, q pgxQuerier, uuid string, forUpdate bool) (unitRow, error) {
	sql := `SELECT inventory_slab_id, inventory_item_id, warehouse_id,
	               inventory_bin_id, inventory_bundle_id, slab_status, slab_area
	        FROM inventory_slab
	        WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var u unitRow
	err := q.QueryRow(ctx, sql, uuid).Scan(
		&u.id, &u.itemID, &u.warehouseID, &u.binID, &u.bundleID, &u.status, &u.area)
	if errors.Is(err, pgx.ErrNoRows) {
		return unitRow{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return unitRow{}, ErrNotFound
		}
		return unitRow{}, fmt.Errorf("resolve inventory unit: %w", err)
	}
	return u, nil
}

// itemUnitInfo carries what the unit store needs to know about a unit's parent
// catalogue item to compute area correctly.
type itemUnitInfo struct {
	itemID       int
	unitID       int
	unitCode     string
	unitCategory string
	tracking     string
}

// resolveItemForUnit loads the parent item's unit of measure.
//
// The unit's area MUST be expressed in the item's own unit, because
// inventory_slab_ledger.quantity_delta is defined that way and feeds
// inventory_stock. Reading the item's unit here — rather than trusting
// slab_area_unit_id from the caller — is what keeps a SQM measurement from
// being ledgered against a SQFT item.
func resolveItemForUnit(ctx context.Context, q pgxQuerier, itemUUID string) (itemUnitInfo, error) {
	var info itemUnitInfo
	err := q.QueryRow(ctx, `
		SELECT ii.inventory_item_id, u.unit_id, u.unit_code, u.unit_category, ii.inventory_item_tracking
		FROM inventory_item ii
		JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`,
		itemUUID).Scan(&info.itemID, &info.unitID, &info.unitCode, &info.unitCategory, &info.tracking)
	if errors.Is(err, pgx.ErrNoRows) {
		return itemUnitInfo{}, ClientError{Msg: "The referenced inventory item does not exist."}
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return itemUnitInfo{}, ClientError{Msg: "The referenced inventory item does not exist."}
		}
		return itemUnitInfo{}, fmt.Errorf("resolve unit item: %w", err)
	}
	return info, nil
}

// mapUnitWriteErr turns a constraint violation from a unit write into a
// client-facing 400 where one is warranted.
func mapUnitWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "A live unit with that serial, barcode, or vendor and supplier code already exists."}
	case isFKViolation(err):
		return ClientError{Msg: "An invalid item, vendor, warehouse, bin or bundle was referenced."}
	case isCheckViolation(err):
		return ClientError{Msg: "One or more unit values are out of range."}
	}
	return fmt.Errorf("%s inventory unit: %w", verb, err)
}

// writeUnitHistory appends one row to inventory_unit_history, the OPERATIONAL
// trail for a physical piece.
//
// Deliberately separate from inventory_slab_ledger, which is the FINANCIAL
// record: bin moves, re-grades and photo swaps change no quantity, so they must
// not touch the ledger. Writing a zero-delta row there would collide with its
// once-only partial indexes and pollute the stock audit with non-events.
func writeUnitHistory(ctx context.Context, tx pgx.Tx, unitID int, action, field, oldVal, newVal string,
	fromBin, toBin *int, reasonID *int, note string, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_unit_history (
			inventory_slab_id, history_action, history_field,
			history_old_value, history_new_value,
			from_bin_id, to_bin_id, inventory_reason_id, history_note, history_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		unitID, action, field, oldVal, newVal,
		fromBin, toBin, reasonID, note, nullableInt(actorEmployeeID),
	); err != nil {
		return fmt.Errorf("write inventory unit history: %w", err)
	}
	return nil
}

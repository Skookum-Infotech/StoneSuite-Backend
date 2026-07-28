package inventory

// unit_store_write.go — create, bin move and scrap for physical units.
// Moved here from fabrication/slab_store.go.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateUnit receives a physical piece into stock: resolves its parent item,
// computes the area in the ITEM's unit, inserts the unit, and increments stock
// through a 'received' ledger row — the only external way serialized stock grows.
func CreateUnit(ctx context.Context, pool *pgxpool.Pool, in CreateUnitInput, actorEmployeeID int) (*Unit, error) {
	if strings.TrimSpace(in.Serial) == "" {
		return nil, ClientError{Msg: "A unit serial is required."}
	}
	if in.LengthMM <= 0 || in.WidthMM <= 0 || in.ThicknessMM <= 0 {
		return nil, ClientError{Msg: "Unit dimensions must be positive."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create inventory unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	item, err := resolveItemForUnit(ctx, tx, in.InventoryItemUUID)
	if err != nil {
		return nil, err
	}
	// A count unit like SLAB would make offcut recovery produce a fractional
	// count, so serialized stock must be area-denominated.
	if item.unitCategory != UnitCategoryArea {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Serialized items must use an area unit; %s is a %s unit.", item.unitCode, item.unitCategory)}
	}
	// Computed from the millimetres into the item's own unit. in.Area is
	// deliberately not used — see the note on CreateUnitInput.Area.
	area, err := AreaFor(in.LengthMM, in.WidthMM, item.unitCode, item.unitCategory)
	if err != nil {
		return nil, err
	}

	binID, err := resolveUnitBin(ctx, tx, in.BinUUID, in.WarehouseID)
	if err != nil {
		return nil, err
	}
	bundleID, err := resolveUnitBundle(ctx, tx, in.BundleUUID)
	if err != nil {
		return nil, err
	}

	var (
		unitID  int
		newUUID string
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_slab (
			slab_serial, slab_unit_kind, slab_vendor_id, slab_supplier_code, slab_barcode,
			slab_received_at, slab_received_by,
			inventory_item_id, warehouse_id, inventory_bin_id, inventory_bundle_id,
			slab_bundle_id, slab_block_id, slab_lot,
			slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id,
			slab_form, slab_status, slab_grade, slab_finish, slab_finish_id, slab_created_by)
		VALUES ($1,$2,$3,$4,$5, CURRENT_DATE,$6, $7,$8,$9,$10, $11,$12,$13,
			$14,$15,$16,$17,$18, 'full','available',$19,$20,$21,$6)
		RETURNING inventory_slab_id, inventory_slab_uuid`,
		in.Serial, UnitKindSlab, nullableIntPtr(in.VendorID), in.SupplierCode, in.Barcode,
		nullableInt(actorEmployeeID),
		item.itemID, in.WarehouseID, binID, bundleID,
		in.BundleID, in.BlockID, in.Lot,
		in.LengthMM, in.WidthMM, in.ThicknessMM, area, item.unitID,
		in.Grade, in.Finish, nullableIntPtr(in.FinishID),
	).Scan(&unitID, &newUUID)
	if err != nil {
		return nil, mapUnitWriteErr(err, "insert")
	}

	if err := SlabLedgerAndStock(ctx, tx, unitID, item.itemID, in.WarehouseID,
		EventReceived, area, nil, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := writeUnitHistory(ctx, tx, unitID, "create", "", "", in.Serial,
		nil, binID, nil, "", actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create inventory unit: %w", err)
	}
	return GetUnit(ctx, pool, newUUID)
}

// resolveUnitBin validates a bin belongs to the unit's warehouse.
func resolveUnitBin(ctx context.Context, tx pgx.Tx, binUUID *string, warehouseID int) (*int, error) {
	if binUUID == nil || strings.TrimSpace(*binUUID) == "" {
		return nil, nil
	}
	b, err := binByUUID(ctx, tx, *binUUID, false)
	if err != nil {
		if err == ErrNotFound {
			return nil, ClientError{Msg: "Unknown bin."}
		}
		return nil, err
	}
	if b.warehouseID != warehouseID {
		return nil, ClientError{Msg: "That bin is in a different warehouse."}
	}
	return &b.id, nil
}

func resolveUnitBundle(ctx context.Context, tx pgx.Tx, bundleUUID *string) (*int, error) {
	if bundleUUID == nil || strings.TrimSpace(*bundleUUID) == "" {
		return nil, nil
	}
	var id int
	err := tx.QueryRow(ctx, `
		SELECT inventory_bundle_id FROM inventory_bundle
		WHERE inventory_bundle_uuid = $1 AND bundle_deleted_at IS NULL`, *bundleUUID).Scan(&id)
	if err != nil {
		return nil, ClientError{Msg: "Unknown bundle."}
	}
	return &id, nil
}

// MoveUnitToBin relocates a unit within its warehouse.
//
// This writes NO ledger row. Bins locate serialized units only and
// inventory_stock is keyed on (item, warehouse), so a bin move is stock-neutral
// by construction — it is an operational event, recorded in
// inventory_unit_history alone.
func MoveUnitToBin(ctx context.Context, pool *pgxpool.Pool, uuid string, in MoveUnitInput, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin move unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, err := unitByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	if u.status == StatusConsumed || u.status == StatusScrapped {
		return ClientError{Msg: "A consumed or scrapped unit cannot be moved."}
	}
	if u.status == StatusInTransit {
		// It is on a truck between two warehouses and holds no bin at all, so a
		// bin move here would file it into a rack it has not reached.
		return ClientError{Msg: "This unit is in transit. Receive its transfer before moving it to a bin."}
	}
	// A sealed bundle is physically banded to a pallet: moving one member means
	// cutting the bands. Cross-row, so no CHECK can express it.
	if u.bundleID != nil {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_id = $1`, *u.bundleID).Scan(&status); err != nil {
			return fmt.Errorf("read bundle status: %w", err)
		}
		if status == "sealed" {
			return ClientError{Msg: "This unit is in a sealed bundle. Move the whole bundle, or break it first."}
		}
	}

	newBin, err := resolveUnitBin(ctx, tx, in.BinUUID, u.warehouseID)
	if err != nil {
		return err
	}
	// BOTH ends are checked against a live cycle count. Guarding only the source
	// would still let stock be moved INTO a frozen bin, which corrupts that
	// bin's count just as thoroughly as moving stock out of it.
	if err := checkBinMoveNotFrozen(ctx, tx, u.warehouseID, u.binID, newBin); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET inventory_bin_id = $2, slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`, u.id, newBin, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "move")
	}
	if err := writeUnitHistory(ctx, tx, u.id, "bin_move", "binId",
		binLabel(u.binID), binLabel(newBin), u.binID, newBin, nil, in.Note, actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move unit: %w", err)
	}
	return nil
}

func binLabel(id *int) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%d", *id)
}

// ScrapUnit writes a unit off. If it was still counted in stock (available or
// reserved) the scrap decrements stock through a 'scrapped' ledger row; an
// already-consumed unit is a no-op on stock, because it was deducted when it
// was consumed.
func ScrapUnit(ctx context.Context, pool *pgxpool.Pool, uuid string, reasonID *int, note string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin scrap unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, err := unitByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	if u.status == StatusScrapped {
		return ClientError{Msg: "This unit is already scrapped."}
	}
	if u.status == StatusInTransit {
		// Writing it off mid-transfer would deduct stock the ship leg has already
		// deducted, and leave the receive leg with nothing to land.
		return ClientError{Msg: "This unit is in transit. Receive or cancel its transfer before scrapping it."}
	}

	if u.status == "reserved" {
		// Release the live fabrication reservation first, or a later consume
		// would deduct stock for a unit that has already been written off.
		//
		// This is inventory reaching into a fabrication-owned table, and it is
		// deliberate: the integrity rule ("a scrapped unit holds no
		// reservation") belongs to whoever owns the unit's status. When a
		// second reservation source appears, this becomes a registered hook
		// rather than a second hard-coded UPDATE.
		if _, err := tx.Exec(ctx, `
			UPDATE fabrication_job_slab SET allocation_status = 'released'
			WHERE inventory_slab_id = $1 AND allocation_status = 'reserved'`, u.id); err != nil {
			return fmt.Errorf("release reservation on scrap: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'scrapped', slab_updated_at = NOW(), slab_updated_by = $2
		WHERE inventory_slab_id = $1`, u.id, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("mark unit scrapped: %w", err)
	}
	// Only available/reserved stone is still counted; consumed already left.
	if u.status == "available" || u.status == "reserved" {
		if err := SlabLedgerAndStock(ctx, tx, u.id, u.itemID, u.warehouseID,
			EventScrapped, -u.area, nil, actorEmployeeID); err != nil {
			return err
		}
	}
	if err := writeUnitHistory(ctx, tx, u.id, "scrap", "status", u.status, "scrapped",
		nil, nil, reasonID, note, actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit scrap unit: %w", err)
	}
	return nil
}

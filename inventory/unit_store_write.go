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

// CreateUnit receives a physical piece into stock outside a purchase-order
// receipt: it resolves the piece's item, bin, bundle and vendor from their
// uuids, then delegates to ReceiveUnitTx in its own transaction.
//
// It is no longer reachable over HTTP -- a receipt against a purchase order is
// the only way new slabs enter stock through the app -- and remains for
// fixtures and any future non-HTTP caller such as a data importer.
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
	binID, err := resolveUnitBin(ctx, tx, in.BinUUID, in.WarehouseID)
	if err != nil {
		return nil, err
	}
	bundleID, err := resolveUnitBundle(ctx, tx, in.BundleUUID)
	if err != nil {
		return nil, err
	}
	vendorID, err := resolveUnitVendor(ctx, tx, in.VendorUUID)
	if err != nil {
		return nil, err
	}

	rec, err := ReceiveUnitTx(ctx, tx, ReceiveUnitParams{
		Serial:       in.Serial,
		VendorID:     vendorID,
		SupplierCode: in.SupplierCode,
		Barcode:      in.Barcode,
		Item: ItemUnitInfo{
			ItemID: item.itemID, UnitID: item.unitID, UnitCode: item.unitCode,
			UnitCategory: item.unitCategory, Tracking: item.tracking,
		},
		WarehouseID: in.WarehouseID,
		BinID:       binID,
		BundleID:    bundleID,
		BundleLabel: in.BundleID,
		BlockID:     in.BlockID,
		Lot:         in.Lot,
		LengthMM:    in.LengthMM,
		WidthMM:     in.WidthMM,
		ThicknessMM: in.ThicknessMM,
		Grade:       in.Grade,
		Finish:      in.Finish,
		FinishID:    in.FinishID,
	}, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create inventory unit: %w", err)
	}
	return GetUnit(ctx, pool, rec.UUID)
}

// resolveUnitBin validates a bin belongs to the unit's warehouse.
func resolveUnitBin(ctx context.Context, q pgxQuerier, binUUID *string, warehouseID int) (*int, error) {
	if binUUID == nil || strings.TrimSpace(*binUUID) == "" {
		return nil, nil
	}
	b, err := binByUUID(ctx, q, *binUUID, false)
	if err != nil {
		if err == ErrNotFound {
			return nil, ClientError{Msg: "Unknown bin."}
		}
		return nil, err
	}
	if b.warehouseID != warehouseID {
		return nil, ClientError{Msg: "That bin is in a different location."}
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

// resolveUnitVendor mirrors purchaseorder's vendorSnapshot: the client only
// ever knows a vendor by its uuid (vendors/types.go never exposes the
// internal SERIAL), so this is the only place that resolves one to the
// vendor_id inventory_slab actually stores.
func resolveUnitVendor(ctx context.Context, tx pgx.Tx, vendorUUID *string) (*int, error) {
	if vendorUUID == nil || strings.TrimSpace(*vendorUUID) == "" {
		return nil, nil
	}
	var id int
	err := tx.QueryRow(ctx, `
		SELECT vendor_id FROM vendor
		WHERE vendor_uuid = $1 AND vendor_deleted_at IS NULL`, *vendorUUID).Scan(&id)
	if err != nil {
		return nil, ClientError{Msg: "Unknown vendor."}
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

	if err := MoveUnitToBinTx(ctx, tx, uuid, in, actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move unit: %w", err)
	}
	return nil
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

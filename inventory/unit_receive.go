package inventory

// unit_receive.go — the transaction-level writers for bringing a physical unit
// into stock and for taking a received one back out.
//
// A purchase-order receipt is the only way a new slab enters stock, and it must
// do so in the SAME transaction that posts the receipt. These helpers therefore
// take a caller-owned pgx.Tx and never begin or commit one. CreateUnit (the
// pool-level wrapper below the receipt path, still used by fixtures and other
// non-HTTP callers) is a thin resolve-then-ReceiveUnitTx.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ItemUnitInfo is what a caller needs to know about a catalogue item to decide
// whether it is slab-tracked and to compute a piece's area in the item's unit.
type ItemUnitInfo struct {
	ItemID       int
	UnitID       int
	UnitCode     string
	UnitCategory string
	Tracking     string
}

// IsSerialized reports whether the item is tracked as individual units.
func (i ItemUnitInfo) IsSerialized() bool { return i.Tracking == TrackingSerialized }

// ResolveItemUnitByID loads an item's tracking mode and unit of measure by its
// internal id.
//
// A soft-deleted item still resolves: a purchase order line already points at
// it, and a receipt for goods that physically arrived must not be blocked by a
// later catalogue clean-up.
func ResolveItemUnitByID(ctx context.Context, q pgxQuerier, itemID int) (ItemUnitInfo, error) {
	info := ItemUnitInfo{ItemID: itemID}
	err := q.QueryRow(ctx, `
		SELECT u.unit_id, u.unit_code, u.unit_category, ii.inventory_item_tracking
		FROM inventory_item ii
		JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_id = $1`,
		itemID).Scan(&info.UnitID, &info.UnitCode, &info.UnitCategory, &info.Tracking)
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemUnitInfo{}, ClientError{Msg: "The referenced inventory item does not exist."}
	}
	if err != nil {
		return ItemUnitInfo{}, fmt.Errorf("resolve item unit of measure: %w", err)
	}
	return info, nil
}

// ResolveBinForWarehouse resolves a bin uuid to its internal id, refusing a bin
// that belongs to a different warehouse. A blank uuid means "no bin" (nil).
func ResolveBinForWarehouse(ctx context.Context, q pgxQuerier, binUUID string, warehouseID int) (*int, error) {
	if strings.TrimSpace(binUUID) == "" {
		return nil, nil
	}
	return resolveUnitBin(ctx, q, &binUUID, warehouseID)
}

// ReceiveUnitParams is one full physical piece to bring into stock, expressed
// with internal ids — the caller has already resolved every uuid.
type ReceiveUnitParams struct {
	Serial       string
	VendorID     *int
	SupplierCode string
	Barcode      string

	Item        ItemUnitInfo
	WarehouseID int
	BinID       *int
	BundleID    *int
	BundleLabel string
	BlockID     string
	Lot         string

	LengthMM    float64
	WidthMM     float64
	ThicknessMM float64
	Grade       string
	Finish      string
	FinishID    *int

	// ReceivedAt is "yyyy-mm-dd"; blank means today.
	ReceivedAt string
}

// ReceivedUnit identifies the unit ReceiveUnitTx created.
type ReceivedUnit struct {
	ID   int
	UUID string
	// Area is what the unit contributed to stock, in the item's own unit.
	Area float64
}

// ReceiveUnitTx inserts one unit, increments stock through a 'received' ledger
// row (the only external way serialized stock grows) and writes its history —
// all inside the caller's transaction.
func ReceiveUnitTx(ctx context.Context, tx pgx.Tx, p ReceiveUnitParams, actorEmployeeID int) (*ReceivedUnit, error) {
	if strings.TrimSpace(p.Serial) == "" {
		return nil, ClientError{Msg: "A unit serial is required."}
	}
	if p.LengthMM <= 0 || p.WidthMM <= 0 || p.ThicknessMM <= 0 {
		return nil, ClientError{Msg: "Unit dimensions must be positive."}
	}
	// A count unit like SLAB would make offcut recovery produce a fractional
	// count, so serialized stock must be area-denominated.
	if p.Item.UnitCategory != UnitCategoryArea {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Serialized items must use an area unit; %s is a %s unit.", p.Item.UnitCode, p.Item.UnitCategory)}
	}
	// Computed from the millimetres into the item's own unit — a client-supplied
	// area is never trusted (see the note on CreateUnitInput.Area).
	area, err := AreaFor(p.LengthMM, p.WidthMM, p.Item.UnitCode, p.Item.UnitCategory)
	if err != nil {
		return nil, err
	}

	var rec ReceivedUnit
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_slab (
			slab_serial, slab_unit_kind, slab_vendor_id, slab_supplier_code, slab_barcode,
			slab_received_at, slab_received_by,
			inventory_item_id, warehouse_id, inventory_bin_id, inventory_bundle_id,
			slab_bundle_id, slab_block_id, slab_lot,
			slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id,
			slab_form, slab_status, slab_grade, slab_finish, slab_finish_id, slab_created_by)
		VALUES ($1,$2,$3,$4,$5, COALESCE(NULLIF($22,'')::date, CURRENT_DATE),$6, $7,$8,$9,$10, $11,$12,$13,
			$14,$15,$16,$17,$18, 'full','available',$19,$20,$21,$6)
		RETURNING inventory_slab_id, inventory_slab_uuid`,
		p.Serial, UnitKindSlab, p.VendorID, p.SupplierCode, p.Barcode,
		nullableInt(actorEmployeeID),
		p.Item.ItemID, p.WarehouseID, p.BinID, p.BundleID,
		p.BundleLabel, p.BlockID, p.Lot,
		p.LengthMM, p.WidthMM, p.ThicknessMM, area, p.Item.UnitID,
		p.Grade, p.Finish, nullableIntPtr(p.FinishID),
		p.ReceivedAt,
	).Scan(&rec.ID, &rec.UUID)
	if err != nil {
		return nil, mapUnitWriteErr(err, "insert")
	}
	rec.Area = area

	if err := SlabLedgerAndStock(ctx, tx, rec.ID, p.Item.ItemID, p.WarehouseID,
		EventReceived, area, nil, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := writeUnitHistory(ctx, tx, rec.ID, "create", "", "", p.Serial,
		nil, p.BinID, nil, "", actorEmployeeID); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ReverseReceivedUnitsTx takes units that a receipt brought in back out of
// stock: each is scrapped and its area leaves stock through a 'scrapped' ledger
// row. It is what voiding a posted receipt does to its slabs.
//
// A unit is only reversible while it is exactly as it was received: available,
// still in the receiving warehouse and not banded into a bundle. Anything else
// (reserved for a job, cut, consumed, scrapped, transferred) means the stone has
// moved on, and silently writing it off would corrupt whatever now depends on
// it. Every offending serial is named so the user can fix them in one pass; no
// unit is touched unless all of them are reversible.
func ReverseReceivedUnitsTx(ctx context.Context, tx pgx.Tx, slabIDs []int, warehouseID int, note string, actorEmployeeID int) error {
	if len(slabIDs) == 0 {
		return nil
	}
	type held struct {
		id     int
		itemID int
		area   float64
		binID  *int
	}
	rows, err := tx.Query(ctx, `
		SELECT inventory_slab_id, inventory_item_id, slab_serial, slab_status, warehouse_id,
		       slab_area, inventory_bin_id, inventory_bundle_id
		FROM inventory_slab
		WHERE inventory_slab_id = ANY($1) AND slab_deleted_at IS NULL
		ORDER BY inventory_slab_id
		FOR UPDATE`, slabIDs)
	if err != nil {
		return fmt.Errorf("lock received units for reversal: %w", err)
	}
	var (
		toReverse []held
		blockers  []string
	)
	for rows.Next() {
		var (
			h        held
			serial   string
			status   string
			whID     int
			bundleID *int
		)
		if err := rows.Scan(&h.id, &h.itemID, &serial, &status, &whID, &h.area, &h.binID, &bundleID); err != nil {
			rows.Close()
			return fmt.Errorf("scan received unit for reversal: %w", err)
		}
		switch {
		case status != StatusAvailable:
			blockers = append(blockers, fmt.Sprintf("%s (%s)", serial, status))
		case whID != warehouseID:
			blockers = append(blockers, fmt.Sprintf("%s (moved to another warehouse)", serial))
		case bundleID != nil:
			blockers = append(blockers, fmt.Sprintf("%s (in a bundle)", serial))
		default:
			toReverse = append(toReverse, h)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("lock received units for reversal: %w", err)
	}
	if len(blockers) > 0 {
		return ClientError{Msg: "This receipt cannot be voided because slabs it received have since been used or moved: " +
			strings.Join(blockers, ", ") + "."}
	}

	for _, h := range toReverse {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_slab SET slab_status = $2, slab_updated_at = NOW(), slab_updated_by = $3
			WHERE inventory_slab_id = $1`, h.id, StatusScrapped, nullableInt(actorEmployeeID)); err != nil {
			return mapUnitWriteErr(err, "reverse")
		}
		if err := SlabLedgerAndStock(ctx, tx, h.id, h.itemID, warehouseID,
			EventScrapped, -h.area, nil, actorEmployeeID); err != nil {
			return err
		}
		if err := writeUnitHistory(ctx, tx, h.id, "scrap", "status", StatusAvailable, StatusScrapped,
			h.binID, h.binID, nil, note, actorEmployeeID); err != nil {
			return err
		}
	}
	return nil
}

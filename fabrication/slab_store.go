package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateSlabInput is the slab-catalog create payload. A slab is received as a
// full stone; offcuts are minted internally by recovery, never via this path.
type CreateSlabInput struct {
	Serial            string  `json:"serial"`
	VendorID          *int    `json:"vendorId"`
	SupplierCode      string  `json:"supplierCode"`
	InventoryItemUUID string  `json:"inventoryItemUuid"`
	WarehouseID       int     `json:"warehouseId"`
	BundleID          string  `json:"bundleId"`
	BlockID           string  `json:"blockId"`
	Lot               string  `json:"lot"`
	LengthMM          float64 `json:"lengthMm"`
	WidthMM           float64 `json:"widthMm"`
	ThicknessMM       float64 `json:"thicknessMm"`
	Area              float64 `json:"area"`
	Grade             string  `json:"grade"`
	Finish            string  `json:"finish"`
}

// CreateSlab receives a full physical slab: validates the parent item is
// area-denominated (§4.11), inserts the slab, and increments stock by its area
// via a 'received' ledger row (§4.1a) — the only external way slab stock grows.
func CreateSlab(ctx context.Context, pool *pgxpool.Pool, in CreateSlabInput, actorEmployeeID int) (*Slab, error) {
	if in.Serial == "" {
		return nil, ClientError{Msg: "A slab serial is required."}
	}
	if in.LengthMM <= 0 || in.WidthMM <= 0 || in.ThicknessMM <= 0 || in.Area <= 0 {
		return nil, ClientError{Msg: "Slab dimensions and area must be positive."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create slab: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Resolve the item and assert an area unit (§4.11): a count unit like SLAB
	// would make offcut recovery produce a fractional count.
	var itemID, unitID int
	var unitCategory, unitCode string
	err = tx.QueryRow(ctx, `
		SELECT ii.inventory_item_id, u.unit_id, u.unit_category, u.unit_code
		FROM inventory_item ii JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`,
		in.InventoryItemUUID).Scan(&itemID, &unitID, &unitCategory, &unitCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "The referenced inventory item does not exist."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve slab item: %w", err)
	}
	if unitCategory != "area" {
		return nil, ClientError{Msg: fmt.Sprintf("Slab-tracked items must use an area unit; %s is a %s unit.", unitCode, unitCategory)}
	}

	var slabID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_slab (
			slab_serial, slab_vendor_id, slab_supplier_code, slab_received_at, slab_received_by,
			inventory_item_id, warehouse_id, slab_bundle_id, slab_block_id, slab_lot,
			slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id,
			slab_form, slab_status, slab_grade, slab_finish, slab_created_by)
		VALUES ($1,$2,$3,CURRENT_DATE,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14,
			'full','available',$15,$16,$4)
		RETURNING inventory_slab_id, inventory_slab_uuid`,
		in.Serial, nullableIntPtr(in.VendorID), in.SupplierCode, nullableInt(actorEmployeeID),
		itemID, in.WarehouseID, in.BundleID, in.BlockID, in.Lot,
		in.LengthMM, in.WidthMM, in.ThicknessMM, in.Area, unitID,
		in.Grade, in.Finish).Scan(&slabID, &newUUID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ClientError{Msg: "A live slab with that serial, or that vendor+supplier code, already exists."}
		}
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "An invalid vendor or warehouse was referenced."}
		}
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more slab values are out of range."}
		}
		return nil, fmt.Errorf("insert slab: %w", err)
	}

	// Receipt increments stock (§4.1a).
	if err := ledgerAndStock(ctx, tx, slabID, itemID, in.WarehouseID, ledgerReceived, in.Area, nil, actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create slab: %w", err)
	}
	return GetSlab(ctx, pool, newUUID)
}

// GetSlab loads one slab by uuid.
func GetSlab(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Slab, error) {
	var s Slab
	err := pool.QueryRow(ctx, `
		SELECT s.inventory_slab_uuid, s.slab_serial, s.slab_vendor_id, s.slab_supplier_code,
		       ii.inventory_item_uuid, s.warehouse_id, s.slab_bundle_id,
		       s.slab_length_mm, s.slab_width_mm, s.slab_thickness_mm, s.slab_area,
		       s.slab_form, s.slab_status, s.slab_grade, s.slab_finish
		FROM inventory_slab s JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		WHERE s.inventory_slab_uuid = $1 AND s.slab_deleted_at IS NULL`, uuid).Scan(
		&s.ID, &s.Serial, &s.VendorID, &s.SupplierCode,
		&s.InventoryItemID, &s.WarehouseID, &s.BundleID,
		&s.LengthMM, &s.WidthMM, &s.ThicknessMM, &s.Area,
		&s.Form, &s.Status, &s.Grade, &s.Finish)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get slab: %w", err)
	}
	return &s, nil
}

// ScrapSlab writes off a slab. If it was still counted in stock (available or
// reserved) the scrap decrements stock via a 'scrapped' ledger row; an
// already-consumed slab is a no-op on stock (it was deducted at CUTG). A
// reserved slab's live allocation is released so a later CUTG cannot
// double-deduct it (§4.9).
func ScrapSlab(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin scrap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var slabID, itemID, whID int
	var status string
	var area float64
	err = tx.QueryRow(ctx, `
		SELECT inventory_slab_id, inventory_item_id, warehouse_id, slab_status, slab_area
		FROM inventory_slab WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&slabID, &itemID, &whID, &status, &area)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock slab for scrap: %w", err)
	}
	if status == "scrapped" {
		return ClientError{Msg: "Slab is already scrapped."}
	}

	if status == "reserved" {
		// Release the live allocation first so CUTG can't deduct it later.
		if _, err := tx.Exec(ctx, `
			UPDATE fabrication_job_slab SET allocation_status = 'released'
			WHERE inventory_slab_id = $1 AND allocation_status = 'reserved'`, slabID); err != nil {
			return fmt.Errorf("release reservation on scrap: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'scrapped', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, slabID); err != nil {
		return fmt.Errorf("mark slab scrapped: %w", err)
	}
	// Only available/reserved stone is still counted; consumed already left.
	if status == "available" || status == "reserved" {
		if err := ledgerAndStock(ctx, tx, slabID, itemID, whID, ledgerScrapped, -area, nil, actorEmployeeID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit scrap: %w", err)
	}
	return nil
}

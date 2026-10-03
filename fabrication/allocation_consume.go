package fabrication

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/inventory"
)

// consumeSlabs marks every reserved slab on a job consumed and decrements stock
// once per slab via a ledger row — the deduct at CUTG (§4.1). Idempotent: the
// uq_slab_ledger_consumed index means a re-run cannot double-deduct.
func consumeSlabs(ctx context.Context, tx pgx.Tx, jobInternalID, salesOrderID, actorEmployeeID int) error {
	if err := requireUnstartedMaterial(ctx, tx, jobInternalID, 0); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT fjs.fabrication_job_slab_id, s.inventory_slab_id, s.inventory_item_id, s.warehouse_id, s.slab_area
		FROM fabrication_job_slab fjs
		JOIN inventory_slab s ON s.inventory_slab_id = fjs.inventory_slab_id
		WHERE fjs.fabrication_job_id = $1 AND fjs.allocation_status = 'reserved'
		ORDER BY s.inventory_slab_id
		FOR UPDATE OF fjs, s`, jobInternalID)
	if err != nil {
		return fmt.Errorf("load slabs to consume: %w", err)
	}
	type slabRow struct {
		allocID, slabID, itemID, whID int
		area                          float64
	}
	var toConsume []slabRow
	for rows.Next() {
		var sr slabRow
		if err := rows.Scan(&sr.allocID, &sr.slabID, &sr.itemID, &sr.whID, &sr.area); err != nil {
			rows.Close()
			return err
		}
		toConsume = append(toConsume, sr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, sr := range toConsume {
		if _, err := tx.Exec(ctx, `
			UPDATE fabrication_job_slab SET allocation_status = 'consumed', consumed_at = NOW(), consumed_by = $2
			WHERE fabrication_job_slab_id = $1`, sr.allocID, nullableInt(actorEmployeeID)); err != nil {
			return fmt.Errorf("mark consumed: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_slab SET slab_status = 'consumed', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, sr.slabID); err != nil {
			return fmt.Errorf("mark slab consumed: %w", err)
		}
		if err := ledgerAndStock(ctx, tx, sr.slabID, sr.itemID, sr.whID, ledgerConsumed, -sr.area, &sr.allocID, actorEmployeeID); err != nil {
			return err
		}
		// The stone has left on-hand, so the order's hold on it shrinks by the same
		// amount — otherwise it would count twice against everyone else's free stock.
		if err := inventory.ApplyConsumption(ctx, tx, salesOrderID, sr.itemID, sr.area); err != nil {
			return err
		}
	}
	return nil
}

// releaseReservedSlabs frees any slab still merely reserved (never consumed),
// used at COMP to close the over-allocation leak (§4.8) and internally by cancel
// before cutting. No stock change — reserved slabs were never deducted.
func releaseReservedSlabs(ctx context.Context, tx pgx.Tx, jobInternalID int) error {
	if err := requireUnstartedMaterial(ctx, tx, jobInternalID, 0); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT fjs.fabrication_job_slab_id, fjs.inventory_slab_id
		FROM fabrication_job_slab fjs
		WHERE fjs.fabrication_job_id = $1 AND fjs.allocation_status = 'reserved'
		FOR UPDATE OF fjs`, jobInternalID)
	if err != nil {
		return fmt.Errorf("load reserved slabs: %w", err)
	}
	type rel struct{ allocID, slabID int }
	var toRelease []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.allocID, &r.slabID); err != nil {
			rows.Close()
			return err
		}
		toRelease = append(toRelease, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range toRelease {
		if _, err := tx.Exec(ctx, `
			UPDATE fabrication_job_slab SET allocation_status = 'released' WHERE fabrication_job_slab_id = $1`, r.allocID); err != nil {
			return fmt.Errorf("release reserved: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_slab SET slab_status = 'available', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, r.slabID); err != nil {
			return fmt.Errorf("free reserved slab: %w", err)
		}
	}
	return nil
}

// ledgerAndStock writes one slab ledger row and applies its signed delta to
// inventory_stock in the same transaction — the invariant that stock equals the
// ledger sum (§2.9). A negative delta that would drive stock below zero surfaces
// as a ClientError (400), never a raw CHECK 500 (§4.6).
//
// The implementation now lives in inventory.SlabLedgerAndStock, shared with the
// serialized-unit store. This wrapper exists only to translate that package's
// ClientError into this module's, so fabrication's callers and its HTTP error
// mapping are unchanged.
func ledgerAndStock(ctx context.Context, tx pgx.Tx, slabID, itemID, warehouseID int, event string, delta float64, fabSlabID *int, actorEmployeeID int) error {
	err := inventory.SlabLedgerAndStock(ctx, tx, slabID, itemID, warehouseID, event, delta, fabSlabID, actorEmployeeID)
	if err != nil && inventory.IsClientError(err) {
		return ClientError{Msg: err.Error()}
	}
	return err
}

// InventoryForJob lists the slabs allocated to a job (any status), for the
// slabs tab. Reads inventory_slab, so the controller enforces inventory_item:read
// in addition to installation:read.
func InventoryForJob(ctx context.Context, pool *pgxpool.Pool, jobUUID string) ([]Slab, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.inventory_slab_uuid, s.slab_serial, s.slab_vendor_id, s.slab_supplier_code,
		       ii.inventory_item_uuid, s.warehouse_id, s.slab_bundle_id,
		       s.slab_length_mm, s.slab_width_mm, s.slab_thickness_mm, s.slab_area,
		       s.slab_form, fjs.allocation_status, s.slab_grade, s.slab_finish
		FROM fabrication_job_slab fjs
		JOIN fabrication_job fj ON fj.fabrication_job_id = fjs.fabrication_job_id
		JOIN inventory_slab s ON s.inventory_slab_id = fjs.inventory_slab_id
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		WHERE fj.fabrication_job_uuid = $1
		ORDER BY s.slab_serial`, jobUUID)
	if err != nil {
		return nil, fmt.Errorf("load job slabs: %w", err)
	}
	defer rows.Close()
	out := []Slab{}
	for rows.Next() {
		var s Slab
		if err := rows.Scan(&s.ID, &s.Serial, &s.VendorID, &s.SupplierCode,
			&s.InventoryItemID, &s.WarehouseID, &s.BundleID,
			&s.LengthMM, &s.WidthMM, &s.ThicknessMM, &s.Area,
			&s.Form, &s.Status, &s.Grade, &s.Finish); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

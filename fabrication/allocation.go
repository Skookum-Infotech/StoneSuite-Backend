package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ledger event codes (inventory_slab_ledger.event) — see spec §2.9.
const (
	ledgerReceived  = "received"
	ledgerConsumed  = "consumed"
	ledgerRecovered = "recovered"
	ledgerScrapped  = "scrapped"
)

// AllocateSlab reserves a specific physical slab against a job, row-locking the
// slab so concurrent reservations can't oversell. Legal from MALC onward,
// including CUTG and later so a broken slab can be replaced on a live job
// (spec §4.9). No stock change — reserving is not deducting (§4.1).
func AllocateSlab(ctx context.Context, pool *pgxpool.Pool, jobUUID, slabUUID string, pieceUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin allocate slab: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, jobUUID)
	if err != nil {
		return err
	}
	if st.statusCode == StatusDraft || st.statusCode == StatusOrderReceived {
		return ClientError{Msg: "Allocate material once the job has reached material allocation."}
	}
	if IsTerminal(st.statusCode) {
		return ClientError{Msg: "Cannot allocate material to a completed or cancelled job."}
	}

	// Lock the slab and check availability (§4.2, §4.3).
	var slabID int
	var slabStatus string
	err = tx.QueryRow(ctx, `
		SELECT inventory_slab_id, slab_status FROM inventory_slab
		WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL
		FOR UPDATE`, slabUUID).Scan(&slabID, &slabStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "Slab not found."}
	}
	if err != nil {
		return fmt.Errorf("lock slab: %w", err)
	}
	if slabStatus != "available" {
		return ClientError{Msg: fmt.Sprintf("Slab is %s and cannot be allocated.", slabStatus)}
	}

	var pieceID any
	if pieceUUID != "" {
		var id int
		err := tx.QueryRow(ctx, `
			SELECT fi.fabrication_job_item_id FROM fabrication_job_item fi
			WHERE fi.fabrication_job_item_uuid = $1 AND fi.fabrication_job_id = $2 AND fi.item_deleted_at IS NULL`,
			pieceUUID, st.internalID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ClientError{Msg: "Piece not found on this job."}
		}
		if err != nil {
			return fmt.Errorf("resolve piece: %w", err)
		}
		pieceID = id
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO fabrication_job_slab (fabrication_job_id, fabrication_job_item_id, inventory_slab_id, allocation_status, reserved_by)
		VALUES ($1, $2, $3, 'reserved', $4)`, st.internalID, pieceID, slabID, nullableInt(actorEmployeeID)); err != nil {
		// The partial unique index is the double-sell backstop → surface as 400.
		if isUniqueViolation(err) {
			return ClientError{Msg: "That slab is already allocated to a job."}
		}
		return fmt.Errorf("insert allocation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'reserved', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, slabID); err != nil {
		return fmt.Errorf("mark slab reserved: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit allocate slab: %w", err)
	}
	return nil
}

// DeallocateSlab releases a still-reserved slab from a job (before cutting). A
// consumed slab cannot be deallocated — its stone is gone (§4.4).
func DeallocateSlab(ctx context.Context, pool *pgxpool.Pool, jobUUID, slabUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin deallocate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var allocID, slabID int
	var allocStatus string
	err = tx.QueryRow(ctx, `
		SELECT fjs.fabrication_job_slab_id, fjs.inventory_slab_id, fjs.allocation_status
		FROM fabrication_job_slab fjs
		JOIN fabrication_job fj ON fj.fabrication_job_id = fjs.fabrication_job_id
		JOIN inventory_slab s ON s.inventory_slab_id = fjs.inventory_slab_id
		WHERE fj.fabrication_job_uuid = $1 AND s.inventory_slab_uuid = $2
		  AND fjs.allocation_status IN ('reserved','consumed')
		FOR UPDATE OF fjs`, jobUUID, slabUUID).Scan(&allocID, &slabID, &allocStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "No live allocation of that slab on this job."}
	}
	if err != nil {
		return fmt.Errorf("load allocation: %w", err)
	}
	if allocStatus == "consumed" {
		return ClientError{Msg: "A cut slab cannot be released; record a disposition instead."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job_slab SET allocation_status = 'released' WHERE fabrication_job_slab_id = $1`, allocID); err != nil {
		return fmt.Errorf("release allocation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'available', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, slabID); err != nil {
		return fmt.Errorf("free slab: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit deallocate: %w", err)
	}
	return nil
}

// consumeSlabs marks every reserved slab on a job consumed and decrements stock
// once per slab via a ledger row — the deduct at CUTG (§4.1). Idempotent: the
// uq_slab_ledger_consumed index means a re-run cannot double-deduct.
func consumeSlabs(ctx context.Context, tx pgx.Tx, jobInternalID, actorEmployeeID int) error {
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
	}
	return nil
}

// releaseReservedSlabs frees any slab still merely reserved (never consumed),
// used at COMP to close the over-allocation leak (§4.8) and internally by cancel
// before cutting. No stock change — reserved slabs were never deducted.
func releaseReservedSlabs(ctx context.Context, tx pgx.Tx, jobInternalID int) error {
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

// ledgerAndStock writes one ledger row and applies its signed delta to
// inventory_stock in the same transaction — the invariant that stock equals the
// ledger sum (§2.9). A negative delta that would drive stock below zero surfaces
// as a ClientError (400), never a raw CHECK 500 (§4.6).
func ledgerAndStock(ctx context.Context, tx pgx.Tx, slabID, itemID, warehouseID int, event string, delta float64, fabSlabID *int, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_slab_ledger (inventory_slab_id, inventory_item_id, warehouse_id, event, quantity_delta, fabrication_job_slab_id, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		slabID, itemID, warehouseID, event, delta, fabSlabID, nullableInt(actorEmployeeID)); err != nil {
		// A duplicate event (e.g. re-running consume) is a no-op, not an error.
		if isUniqueViolation(err) {
			return nil
		}
		return fmt.Errorf("write ledger row: %w", err)
	}
	// Upsert the stock row and apply the delta.
	tag, err := tx.Exec(ctx, `
		UPDATE inventory_stock SET quantity_on_hand = quantity_on_hand + $3, stock_updated_at = NOW()
		WHERE inventory_item_id = $1 AND warehouse_id = $2`, itemID, warehouseID, delta)
	if err != nil {
		if isCheckViolation(err) {
			return ClientError{Msg: "This action would drive stock below zero; the reservation math is inconsistent."}
		}
		return fmt.Errorf("apply stock delta: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// No stock row yet — only valid for a positive (received/recovered) delta.
		if delta < 0 {
			return ClientError{Msg: "No stock on hand for this item at its warehouse."}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
			VALUES ($1, $2, $3)
			ON CONFLICT (inventory_item_id, warehouse_id) DO UPDATE SET quantity_on_hand = inventory_stock.quantity_on_hand + $3`,
			itemID, warehouseID, delta); err != nil {
			return fmt.Errorf("seed stock row: %w", err)
		}
	}
	return nil
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

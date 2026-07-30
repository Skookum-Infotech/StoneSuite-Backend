package inventorycount

// store_freeze.go — building the line set from what the system believes.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
)

// Freeze moves a draft count to CNTG: it snapshots the system quantity onto one
// line per countable thing, stamps count_frozen_at, and from that moment blocks
// movement inside the counted scope.
//
// Everything about the count hangs off this snapshot. Recomputing the system
// quantity at post time instead would absorb every movement made while the crew
// was walking the yard, so a genuine shortage would reconcile itself to zero
// and the write-off would never be raised.
func Freeze(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Count, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin freeze count: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if err := machine.Validate(cur.statusCode, StatusCounting); err != nil {
		if cur.statusCode == StatusCounting {
			return nil, ClientError{Msg: "This count is already counting."}
		}
		return nil, ClientError{Msg: "Only a draft count can be frozen."}
	}
	// One live count per scope. Two frozen counts over the same bins would each
	// snapshot the other's uncommitted variances and post both.
	if err := noOverlappingCount(ctx, tx, cur); err != nil {
		return nil, err
	}

	recordTypeID, err := docflow.RecordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	countingStatusID, err := docflow.StatusIDByCode(ctx, tx, recordTypeID, StatusCounting)
	if err != nil {
		return nil, err
	}

	n, err := snapshotLines(ctx, tx, cur)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ClientError{Msg: "There is no stock in this scope to count."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count SET count_status = $2,
			count_frozen_at = NOW(), count_frozen_by = $3,
			count_updated_at = NOW(), count_updated_by = $3,
			count_record_version = count_record_version + 1
		WHERE inventory_count_id = $1`,
		cur.id, countingStatusID, actorOrSystem(actorEmployeeID)); err != nil {
		return nil, mapWriteErr(err, "freeze")
	}
	if err := writeHistory(ctx, tx, cur.id, "freeze", &cur.statusID, &countingStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit freeze count: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// noOverlappingCount refuses a second live count covering the same stock.
func noOverlappingCount(ctx context.Context, tx pgx.Tx, cur countRow) error {
	var other string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(c.count_number,'a draft count')
		FROM inventory_count c
		LEFT JOIN inventory_bin cb ON cb.inventory_bin_id = c.inventory_bin_id
		WHERE c.inventory_count_id <> $1
		  AND c.count_deleted_at   IS NULL
		  AND c.count_frozen_at    IS NOT NULL
		  AND c.count_posted_at    IS NULL
		  AND c.count_cancelled_at IS NULL
		  AND c.warehouse_id = $2
		  -- Overlap in either direction: a warehouse-wide count contains every
		  -- bin count, and a bin count is contained by any ancestor's.
		  AND (c.inventory_bin_id IS NULL
		       OR $3 = ''
		       OR $3 = cb.bin_path
		       OR $3 LIKE cb.bin_path || '/%'
		       OR cb.bin_path LIKE $3 || '/%')
		LIMIT 1`, cur.id, cur.warehouseID, cur.binPath).Scan(&other)
	if err != nil {
		return nil // pgx.ErrNoRows means no overlap, which is the happy path
	}
	return ClientError{Msg: fmt.Sprintf(
		"Cycle count %s is already counting this location. Finish it first.", other)}
}

// snapshotLines writes one line per countable thing in scope and returns how
// many it wrote.
//
// Bulk stock is only included for a WAREHOUSE-wide count. Bins locate serialized
// units only and inventory_stock is keyed (item, warehouse), so a bulk quantity
// cannot be attributed to one bin — including it in a bin-scoped count would
// invite the crew to count a shelf and have the whole warehouse's quantity
// written off against it.
func snapshotLines(ctx context.Context, tx pgx.Tx, cur countRow) (int, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count_line SET line_deleted_at = NOW()
		WHERE inventory_count_id = $1 AND line_deleted_at IS NULL`, cur.id); err != nil {
		return 0, fmt.Errorf("clear count lines: %w", err)
	}

	// Serialized units in scope, one line each.
	serialized, err := tx.Exec(ctx, `
		INSERT INTO inventory_count_line (
			inventory_count_id, line_number, inventory_item_id, inventory_slab_id,
			inventory_bin_id, item_name, sku, unit_id, unit_code, slab_serial, system_qty)
		SELECT $1,
		       ROW_NUMBER() OVER (ORDER BY COALESCE(b.bin_path,''), s.slab_serial),
		       s.inventory_item_id, s.inventory_slab_id, s.inventory_bin_id,
		       ii.inventory_item_name, ii.inventory_item_sku,
		       ii.inventory_item_unit_id, u.unit_code, s.slab_serial, s.slab_area
		FROM inventory_slab s
		JOIN inventory_item ii    ON ii.inventory_item_id = s.inventory_item_id
		JOIN lkp_unit u           ON u.unit_id = ii.inventory_item_unit_id
		LEFT JOIN inventory_bin b ON b.inventory_bin_id = s.inventory_bin_id
		WHERE s.slab_deleted_at IS NULL
		  AND s.warehouse_id = $2
		  -- 'available' and 'reserved' are physically on the rack and countable.
		  -- 'in_transit' is on a truck, and 'consumed'/'scrapped' have left stock,
		  -- so counting any of them would report a shortage for stone that is
		  -- exactly where the system says it is.
		  AND s.slab_status IN ('available','reserved')
		  AND ($3 = '' OR COALESCE(b.bin_path,'') = $3 OR COALESCE(b.bin_path,'') LIKE $3 || '/%')`,
		cur.id, cur.warehouseID, cur.binPath)
	if err != nil {
		return 0, mapWriteErr(err, "snapshot serialized lines for")
	}
	total := int(serialized.RowsAffected())

	if cur.binID != nil {
		return total, nil
	}

	// Bulk stock, warehouse-wide counts only.
	bulk, err := tx.Exec(ctx, `
		INSERT INTO inventory_count_line (
			inventory_count_id, line_number, inventory_item_id,
			item_name, sku, unit_id, unit_code, system_qty)
		SELECT $1, $2 + ROW_NUMBER() OVER (ORDER BY ii.inventory_item_sku),
		       st.inventory_item_id, ii.inventory_item_name, ii.inventory_item_sku,
		       ii.inventory_item_unit_id, u.unit_code, st.quantity_on_hand
		FROM inventory_stock st
		JOIN inventory_item ii ON ii.inventory_item_id = st.inventory_item_id
		JOIN lkp_unit u        ON u.unit_id = ii.inventory_item_unit_id
		WHERE st.warehouse_id = $3
		  AND ii.inventory_item_deleted_at IS NULL
		  AND ii.inventory_item_tracking <> 'serialized'`,
		cur.id, total, cur.warehouseID)
	if err != nil {
		return 0, mapWriteErr(err, "snapshot bulk lines for")
	}
	return total + int(bulk.RowsAffected()), nil
}

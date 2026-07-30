package inventorycount

// store_count.go — recording what the crew actually found.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

// RecordCounts writes physical counts onto lines of a counting document.
func RecordCounts(ctx context.Context, pool *pgxpool.Pool, uuid string, entries []CountEntry, actorEmployeeID int) (*Count, error) {
	if len(entries) == 0 {
		return nil, ClientError{Msg: "No counts were supplied."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin record counts: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if !AcceptsCounts(cur.statusCode) {
		return nil, ClientError{Msg: "Counts can only be entered while the count is in progress."}
	}

	for i, e := range entries {
		if strings.TrimSpace(e.LineID) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Entry %d names no line.", i+1)}
		}
		qty, err := resolveCountedQty(ctx, tx, cur.id, e)
		if err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE inventory_count_line SET
				counted_qty = $3, counted_at = NOW(), counted_by = $4,
				inventory_reason_id = COALESCE($5, inventory_reason_id),
				line_notes = CASE WHEN $6 = '' THEN line_notes ELSE $6 END,
				line_updated_at = NOW(),
				line_record_version = line_record_version + 1
			WHERE inventory_count_id = $1 AND inventory_count_line_uuid = $2
			  AND line_deleted_at IS NULL`,
			cur.id, e.LineID, qty, nullableInt(actorEmployeeID),
			nullableIntPtr(e.ReasonID), e.Notes)
		if err != nil {
			if isInvalidTextRepresentation(err) {
				return nil, ClientError{Msg: fmt.Sprintf("Entry %d names a line that does not exist.", i+1)}
			}
			return nil, mapWriteErr(err, "record count on")
		}
		if tag.RowsAffected() == 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Entry %d names a line that is not on this count.", i+1)}
		}
	}

	if err := touch(ctx, tx, cur.id, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit record counts: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// resolveCountedQty turns an entry into the number stored on the line.
//
// A serialized line is binary — the slab is on the rack or it is not — so
// `found` is filled in from the slab's own area rather than from a quantity the
// scanner would have to know.
func resolveCountedQty(ctx context.Context, tx pgx.Tx, countID int, e CountEntry) (float64, error) {
	if e.Found != nil {
		var systemQty float64
		var slabID *int
		err := tx.QueryRow(ctx, `
			SELECT system_qty, inventory_slab_id FROM inventory_count_line
			WHERE inventory_count_id = $1 AND inventory_count_line_uuid = $2
			  AND line_deleted_at IS NULL`, countID, e.LineID).Scan(&systemQty, &slabID)
		if err != nil {
			return 0, ClientError{Msg: "That line is not on this count."}
		}
		if slabID == nil {
			return 0, ClientError{Msg: "A quantity-tracked line needs a counted quantity, not found/not-found."}
		}
		if *e.Found {
			return systemQty, nil
		}
		return 0, nil
	}
	if e.CountedQty == nil {
		return 0, ClientError{Msg: "Each entry needs either a counted quantity or found/not-found."}
	}
	if *e.CountedQty < 0 {
		return 0, ClientError{Msg: "A counted quantity cannot be negative."}
	}
	return *e.CountedQty, nil
}

// AddUnexpected records a unit found in the counted scope that the frozen
// snapshot did not contain.
//
// system_qty is 0 and counted_qty is the slab's area, so the generated variance
// is a positive discrepancy. Flagged is_unexpected because it usually means a
// misfiled location rather than found stone, and a reviewer should see the two
// differently.
func AddUnexpected(ctx context.Context, pool *pgxpool.Pool, uuid string, in UnexpectedEntry, actorEmployeeID int) (*Count, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin add unexpected: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if !AcceptsCounts(cur.statusCode) {
		return nil, ClientError{Msg: "Units can only be added while the count is in progress."}
	}
	unit, err := inventory.ResolveUnitForDocument(ctx, tx, in.InventoryUnitID, false)
	if err != nil {
		if err == inventory.ErrNotFound {
			return nil, ClientError{Msg: "That unit does not exist. Receive genuinely new stone instead of counting it in."}
		}
		return nil, err
	}
	if unit.WarehouseID != cur.warehouseID {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Unit %s belongs to another warehouse. Transfer it rather than counting it here.", unit.Serial)}
	}

	var nextLine int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(line_number),0) + 1 FROM inventory_count_line
		WHERE inventory_count_id = $1`, cur.id).Scan(&nextLine); err != nil {
		return nil, fmt.Errorf("next count line number: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_count_line (
			inventory_count_id, line_number, inventory_item_id, inventory_slab_id,
			inventory_bin_id, inventory_reason_id, item_name, sku, unit_id, unit_code,
			slab_serial, system_qty, counted_qty, is_unexpected, counted_at, counted_by, line_notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, 0,$12, TRUE, NOW(),$13,$14)`,
		cur.id, nextLine, unit.ItemID, unit.ID, unit.BinID, nullableIntPtr(in.ReasonID),
		unit.ItemName, unit.SKU, nullableInt(unit.UnitID), unit.UnitCode,
		unit.Serial, unit.Area, nullableInt(actorEmployeeID), in.Notes); err != nil {
		if isUniqueViolation(err) {
			return nil, ClientError{Msg: fmt.Sprintf("Unit %s is already on this count.", unit.Serial)}
		}
		return nil, mapWriteErr(err, "add unexpected unit to")
	}
	if err := touch(ctx, tx, cur.id, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit add unexpected: %w", err)
	}
	return Get(ctx, pool, uuid)
}

func touch(ctx context.Context, tx pgx.Tx, id, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count SET count_updated_at = NOW(), count_updated_by = $2,
			count_record_version = count_record_version + 1
		WHERE inventory_count_id = $1`, id, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("touch count: %w", err)
	}
	return nil
}

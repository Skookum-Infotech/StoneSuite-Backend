package inventory

// bin_store_write.go — bin create, update (including reparent) and delete.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func validateBinInput(in *BinInput) error {
	if strings.TrimSpace(in.WarehouseUUID) == "" {
		return ClientError{Msg: "A warehouse is required."}
	}
	if err := ValidateBinCode(in.Code); err != nil {
		return err
	}
	if in.Type == "" {
		in.Type = "rack"
	}
	if err := ValidateBinType(in.Type); err != nil {
		return err
	}
	if in.CapacityUnits < 0 || in.CapacityArea < 0 {
		return ClientError{Msg: "Capacity cannot be negative."}
	}
	in.Code = strings.TrimSpace(in.Code)
	return nil
}

// resolveBinParent validates a prospective parent and returns its path.
// A parent in a different warehouse is refused here rather than by a CHECK,
// because a row-local CHECK cannot reach another row.
func resolveBinParent(ctx context.Context, tx pgx.Tx, parentUUID *string, warehouseID int) (parentID *int, parentPath string, err error) {
	if parentUUID == nil || strings.TrimSpace(*parentUUID) == "" {
		return nil, "", nil
	}
	p, err := binByUUID(ctx, tx, *parentUUID, true)
	if err != nil {
		if err == ErrNotFound {
			return nil, "", ClientError{Msg: "Unknown parent bin."}
		}
		return nil, "", err
	}
	if p.warehouseID != warehouseID {
		return nil, "", ClientError{Msg: "A bin's parent must be in the same warehouse."}
	}
	return &p.id, p.path, nil
}

// CreateBin inserts a bin, computing its materialised path from its parent.
func CreateBin(ctx context.Context, pool *pgxpool.Pool, in BinInput, actorEmployeeID int) (*Bin, error) {
	if err := validateBinInput(&in); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create bin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	warehouseID, err := warehouseIDByUUID(ctx, tx, in.WarehouseUUID)
	if err != nil {
		return nil, err
	}
	parentID, parentPath, err := resolveBinParent(ctx, tx, in.ParentUUID, warehouseID)
	if err != nil {
		return nil, err
	}
	path, depth, err := BuildPath(parentPath, in.Code)
	if err != nil {
		return nil, err
	}

	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_bin (
			warehouse_id, bin_code, bin_name, bin_type, bin_parent_id,
			bin_path, bin_depth, bin_capacity_units, bin_capacity_area,
			bin_is_active, bin_notes, bin_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING inventory_bin_uuid`,
		warehouseID, in.Code, in.Name, in.Type, parentID,
		path, depth, in.CapacityUnits, in.CapacityArea,
		in.IsActive, in.Notes, nullableInt(actorEmployeeID),
	).Scan(&newUUID)
	if err != nil {
		return nil, mapBinWriteErr(err, "insert")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create bin: %w", err)
	}
	return GetBin(ctx, pool, newUUID)
}

func mapBinWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "A bin with this code already exists in this warehouse."}
	case isFKViolation(err):
		return ClientError{Msg: "Unknown warehouse, parent bin or capacity unit."}
	case isCheckViolation(err):
		return ClientError{Msg: "The bin's type or depth is not allowed."}
	}
	return fmt.Errorf("%s bin: %w", verb, err)
}

// UpdateBin edits a bin and, when its code or parent changes, rewrites the
// materialised path of its entire subtree in the same transaction.
//
// Skipping the subtree rewrite is the subtle failure here: the descendants keep
// their old bin_path, the varchar_pattern_ops prefix index happily keeps
// returning them under the old location, and nothing errors.
func UpdateBin(ctx context.Context, pool *pgxpool.Pool, uuid string, in BinInput, actorEmployeeID int) error {
	if err := validateBinInput(&in); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update bin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := binByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	warehouseID, err := warehouseIDByUUID(ctx, tx, in.WarehouseUUID)
	if err != nil {
		return err
	}
	if warehouseID != cur.warehouseID {
		// Moving a bin between warehouses would move its contents too, which is
		// a stock movement and belongs to the (not yet built) warehouse transfer
		// document, not to a bin edit.
		return ClientError{Msg: "A bin cannot be moved to a different warehouse."}
	}
	parentID, parentPath, err := resolveBinParent(ctx, tx, in.ParentUUID, warehouseID)
	if err != nil {
		return err
	}
	if parentID != nil && *parentID == cur.id {
		return ClientError{Msg: "A bin cannot be its own parent."}
	}
	if WouldCycle(cur.path, parentPath) {
		return ClientError{Msg: "A bin cannot be moved underneath itself."}
	}
	newPath, newDepth, err := BuildPath(parentPath, in.Code)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_bin SET
			bin_code = $2, bin_name = $3, bin_type = $4, bin_parent_id = $5,
			bin_path = $6, bin_depth = $7,
			bin_capacity_units = $8, bin_capacity_area = $9,
			bin_is_active = $10, bin_notes = $11,
			bin_updated_at = NOW(), bin_updated_by = $12,
			bin_record_version = bin_record_version + 1
		WHERE inventory_bin_id = $1`,
		cur.id, in.Code, in.Name, in.Type, parentID, newPath, newDepth,
		in.CapacityUnits, in.CapacityArea, in.IsActive, in.Notes,
		nullableInt(actorEmployeeID)); err != nil {
		return mapBinWriteErr(err, "update")
	}

	if newPath != cur.path {
		if err := repathDescendants(ctx, tx, cur.path, newPath); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update bin: %w", err)
	}
	return nil
}

// repathDescendants rewrites bin_path and bin_depth for every strict descendant
// of a bin that has just moved or been renamed.
func repathDescendants(ctx context.Context, tx pgx.Tx, oldPath, newPath string) error {
	rows, err := tx.Query(ctx, `
		SELECT inventory_bin_id, bin_path FROM inventory_bin
		WHERE bin_path LIKE $1 AND bin_deleted_at IS NULL
		ORDER BY bin_path`, SubtreePrefix(oldPath))
	if err != nil {
		return fmt.Errorf("load bin subtree: %w", err)
	}
	type row struct {
		id   int
		path string
	}
	var subtree []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path); err != nil {
			rows.Close()
			return fmt.Errorf("scan bin subtree: %w", err)
		}
		subtree = append(subtree, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load bin subtree: %w", err)
	}

	for _, r := range subtree {
		p, d, err := RepathSubtree(oldPath, newPath, r.path)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_bin SET bin_path = $2, bin_depth = $3,
			       bin_record_version = bin_record_version + 1
			WHERE inventory_bin_id = $1`, r.id, p, d); err != nil {
			return mapBinWriteErr(err, "repath")
		}
	}
	return nil
}

// DeleteBin soft-deletes a bin, refusing while it still holds units or has
// live children — either would orphan something the UI can no longer reach.
func DeleteBin(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete bin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := binByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	if cur.isSystem {
		// The seeded STAGING bin is re-created by schema.sql on the next boot
		// anyway, so allowing the delete would just produce a confusing
		// reappearance.
		return ClientError{Msg: "A system bin cannot be deleted."}
	}

	var units int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_slab
		WHERE inventory_bin_id = $1 AND slab_deleted_at IS NULL
		  AND slab_status NOT IN ('consumed','scrapped')`, cur.id).Scan(&units); err != nil {
		return fmt.Errorf("check bin contents: %w", err)
	}
	if units > 0 {
		return ClientError{Msg: "This bin still holds inventory. Move it elsewhere before deleting."}
	}

	var children int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_bin
		WHERE bin_parent_id = $1 AND bin_deleted_at IS NULL`, cur.id).Scan(&children); err != nil {
		return fmt.Errorf("check bin children: %w", err)
	}
	if children > 0 {
		return ClientError{Msg: "This bin has child bins. Delete or move them first."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_bin SET bin_deleted_at = NOW(), bin_deleted_by = $2
		WHERE inventory_bin_id = $1`, cur.id, actorOrSystem(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete bin: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete bin: %w", err)
	}
	return nil
}

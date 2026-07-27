package inventory

// bin_store.go — shared bin helpers, single reads and the tree.
// Writes live in bin_store_create.go / bin_store_update.go / bin_store_delete.go.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// binSelect joins the warehouse for its name and counts live units per bin.
//
// The unit count is a correlated subquery rather than a GROUP BY join so that
// bins holding nothing still appear — an inner join on inventory_slab would
// silently hide every empty bin, which is precisely the set a yard crew needs
// when looking for somewhere to put a slab.
const binSelect = `
	SELECT b.inventory_bin_uuid, w.warehouse_uuid, w.warehouse_name,
	       b.bin_code, b.bin_name, b.bin_type,
	       p.inventory_bin_uuid, b.bin_path, b.bin_depth,
	       b.bin_capacity_units, b.bin_capacity_area,
	       b.bin_is_active, b.bin_is_system, b.bin_notes,
	       b.bin_created_at, b.bin_updated_at,
	       (SELECT COUNT(*) FROM inventory_slab s
	         WHERE s.inventory_bin_id = b.inventory_bin_id
	           AND s.slab_deleted_at IS NULL
	           AND s.slab_status NOT IN ('consumed','scrapped'))
	FROM inventory_bin b
	JOIN lkp_warehouse w ON w.warehouse_id = b.warehouse_id
	LEFT JOIN inventory_bin p ON p.inventory_bin_id = b.bin_parent_id`

func scanBin(row pgx.Row) (*Bin, error) {
	var b Bin
	if err := row.Scan(&b.ID, &b.WarehouseID, &b.WarehouseName,
		&b.Code, &b.Name, &b.Type, &b.ParentID, &b.Path, &b.Depth,
		&b.CapacityUnits, &b.CapacityArea, &b.IsActive, &b.IsSystem, &b.Notes,
		&b.CreatedAt, &b.UpdatedAt, &b.UnitCount); err != nil {
		return nil, err
	}
	b.OverCapacity = b.CapacityUnits > 0 && b.UnitCount > b.CapacityUnits
	return &b, nil
}

// GetBin loads one live bin by uuid.
func GetBin(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Bin, error) {
	b, err := scanBin(pool.QueryRow(ctx, binSelect+`
		WHERE b.inventory_bin_uuid = $1 AND b.bin_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get bin: %w", err)
	}
	return b, nil
}

// binRow is the internal identity of a bin, resolved inside a transaction.
type binRow struct {
	id          int
	warehouseID int
	code        string
	path        string
	depth       int
	isSystem    bool
}

// binByUUID resolves a bin uuid to its internal row, locking it when forUpdate
// is set.
//
// Callers that mutate the tree must lock in a deterministic order — parent
// before child — or two concurrent reparents can deadlock.
func binByUUID(ctx context.Context, q pgxQuerier, uuid string, forUpdate bool) (binRow, error) {
	sql := `SELECT inventory_bin_id, warehouse_id, bin_code, bin_path, bin_depth, bin_is_system
	        FROM inventory_bin
	        WHERE inventory_bin_uuid = $1 AND bin_deleted_at IS NULL`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var b binRow
	err := q.QueryRow(ctx, sql, uuid).Scan(&b.id, &b.warehouseID, &b.code, &b.path, &b.depth, &b.isSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return binRow{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return binRow{}, ErrNotFound
		}
		return binRow{}, fmt.Errorf("resolve bin: %w", err)
	}
	return b, nil
}

// warehouseIDByUUID resolves a warehouse uuid to its internal id.
func warehouseIDByUUID(ctx context.Context, q pgxQuerier, uuid string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT warehouse_id FROM lkp_warehouse
		WHERE warehouse_uuid = $1 AND warehouse_deleted_at IS NULL`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: "Unknown warehouse."}
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return 0, ClientError{Msg: "Unknown warehouse."}
		}
		return 0, fmt.Errorf("resolve warehouse: %w", err)
	}
	return id, nil
}

// ListBins returns live bins, optionally restricted to one warehouse, ordered
// by path so the result is already in tree order.
func ListBins(ctx context.Context, pool *pgxpool.Pool, warehouseUUID string, includeInactive bool) ([]Bin, error) {
	q := binSelect + " WHERE b.bin_deleted_at IS NULL"
	args := []any{}
	if warehouseUUID != "" {
		args = append(args, warehouseUUID)
		q += fmt.Sprintf(" AND w.warehouse_uuid = $%d", len(args))
	}
	if !includeInactive {
		q += " AND b.bin_is_active = TRUE"
	}
	q += " ORDER BY w.warehouse_name, b.bin_path"

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ClientError{Msg: "Unknown warehouse."}
		}
		return nil, fmt.Errorf("list bins: %w", err)
	}
	defer rows.Close()
	out := []Bin{}
	for rows.Next() {
		b, err := scanBin(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bin: %w", err)
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bins: %w", err)
	}
	return out, nil
}

// BinTree returns live bins nested by parent.
//
// Assembled in Go from the flat, path-ordered list rather than with a recursive
// CTE: the materialised bin_path already gives the correct order, so one pass
// is enough and the database does no recursion.
func BinTree(ctx context.Context, pool *pgxpool.Pool, warehouseUUID string, includeInactive bool) ([]Bin, error) {
	flat, err := ListBins(ctx, pool, warehouseUUID, includeInactive)
	if err != nil {
		return nil, err
	}
	return assembleBinTree(flat), nil
}

// assembleBinTree nests a path-ordered flat bin list by parent.
//
// Pure, and separated from the query for exactly that reason: the nesting has
// two non-obvious failure modes that are worth a test and impossible to see in
// a SQL result.
//
// It requires flat to be ordered by bin_path ascending, which ListBins
// guarantees, so a parent always precedes its children.
func assembleBinTree(flat []Bin) []Bin {
	byID := make(map[string]*Bin, len(flat))
	for i := range flat {
		byID[flat[i].ID] = &flat[i]
	}

	// Walk BACKWARDS. Going forwards would copy a child into its parent before
	// that child's own children had been attached, and every grandchild would
	// silently vanish from the tree.
	//
	// Prepend rather than append: reverse iteration would otherwise leave each
	// parent's children in descending path order.
	for i := len(flat) - 1; i >= 0; i-- {
		b := &flat[i]
		if b.ParentID == nil {
			continue
		}
		if parent, ok := byID[*b.ParentID]; ok {
			parent.Children = append([]Bin{*b}, parent.Children...)
		}
	}

	// Emit roots in path order. A bin whose parent was filtered out (inactive,
	// or in another warehouse) is surfaced as a root rather than dropped —
	// silently hiding a bin that holds slabs is the worse failure.
	roots := []Bin{}
	for i := range flat {
		b := &flat[i]
		if b.ParentID == nil {
			roots = append(roots, *b)
			continue
		}
		if _, ok := byID[*b.ParentID]; !ok {
			roots = append(roots, *b)
		}
	}
	return roots
}

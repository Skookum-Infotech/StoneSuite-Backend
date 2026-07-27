package inventory

// bundle_store.go — shared bundle helpers, single reads and the member list.
// Writes live in bundle_store_write.go; membership in bundle_members.go.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bundleSelect counts and sums live members with correlated subqueries rather
// than a GROUP BY join, for the same reason binSelect does: an inner join on
// inventory_slab would hide every empty bundle, and an empty bundle is exactly
// what a clerk is looking at while receiving one.
//
// 'Live' excludes consumed and scrapped members, so a bundle's reported area
// tracks what is still physically on the pallet.
const bundleSelect = `
	SELECT bu.inventory_bundle_uuid, bu.bundle_code, bu.bundle_status,
	       bu.bundle_vendor_id, bu.bundle_supplier_code, bu.bundle_block_id, bu.bundle_lot,
	       ii.inventory_item_uuid, COALESCE(ii.inventory_item_name,''),
	       bu.warehouse_id, w.warehouse_name,
	       b.inventory_bin_uuid, COALESCE(b.bin_path,''),
	       to_char(bu.bundle_received_at, 'YYYY-MM-DD'), bu.bundle_notes,
	       (SELECT COUNT(*) FROM inventory_slab s
	         WHERE s.inventory_bundle_id = bu.inventory_bundle_id
	           AND s.slab_deleted_at IS NULL
	           AND s.slab_status NOT IN ('consumed','scrapped')),
	       (SELECT COALESCE(SUM(s.slab_area),0) FROM inventory_slab s
	         WHERE s.inventory_bundle_id = bu.inventory_bundle_id
	           AND s.slab_deleted_at IS NULL
	           AND s.slab_status NOT IN ('consumed','scrapped')),
	       bu.bundle_created_at, bu.bundle_updated_at
	FROM inventory_bundle bu
	JOIN lkp_warehouse w          ON w.warehouse_id = bu.warehouse_id
	LEFT JOIN inventory_item ii   ON ii.inventory_item_id = bu.inventory_item_id
	LEFT JOIN inventory_bin b     ON b.inventory_bin_id = bu.inventory_bin_id`

func scanBundle(row pgx.Row) (*Bundle, error) {
	var b Bundle
	if err := row.Scan(&b.ID, &b.Code, &b.Status,
		&b.VendorID, &b.SupplierCode, &b.BlockID, &b.Lot,
		&b.InventoryItemID, &b.InventoryItemName,
		&b.WarehouseID, &b.WarehouseName, &b.BinID, &b.BinPath,
		&b.ReceivedAt, &b.Notes,
		&b.MemberCount, &b.TotalArea,
		&b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}

// GetBundle loads one live bundle by uuid.
func GetBundle(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Bundle, error) {
	b, err := scanBundle(pool.QueryRow(ctx, bundleSelect+`
		WHERE bu.inventory_bundle_uuid = $1 AND bu.bundle_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get bundle: %w", err)
	}
	return b, nil
}

// ListBundles returns live bundles, newest first, optionally filtered by
// warehouse and status.
func ListBundles(ctx context.Context, pool *pgxpool.Pool, warehouseUUID, status string) ([]Bundle, error) {
	if status != "" {
		if err := validateBundleStatus(status); err != nil {
			return nil, err
		}
	}
	q := bundleSelect + " WHERE bu.bundle_deleted_at IS NULL"
	args := []any{}
	if warehouseUUID != "" {
		args = append(args, warehouseUUID)
		q += fmt.Sprintf(" AND w.warehouse_uuid = $%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND bu.bundle_status = $%d", len(args))
	}
	q += " ORDER BY bu.bundle_created_at DESC, bu.inventory_bundle_id DESC LIMIT 200"

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ClientError{Msg: "Unknown warehouse."}
		}
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	defer rows.Close()
	out := []Bundle{}
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bundle: %w", err)
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	return out, nil
}

// BundleMembers returns the units currently attached to a bundle, including
// consumed and scrapped ones so the pallet's full history stays visible.
func BundleMembers(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]Unit, error) {
	b, err := bundleByUUID(ctx, pool, uuid, false)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, unitSelect+`
		WHERE s.inventory_bundle_id = $1 AND s.slab_deleted_at IS NULL
		ORDER BY s.slab_serial`, b.id)
	if err != nil {
		return nil, fmt.Errorf("list bundle members: %w", err)
	}
	defer rows.Close()
	out := []Unit{}
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bundle member: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bundle members: %w", err)
	}
	return out, nil
}

// bundleRow is a bundle's internal identity, resolved inside a transaction.
type bundleRow struct {
	id          int
	code        string
	status      string
	itemID      *int
	warehouseID int
	binID       *int
}

// bundleByUUID resolves a bundle uuid to its internal row, locking it when
// forUpdate is set.
//
// Every membership change locks the bundle FIRST and its members afterwards, in
// sorted uuid order. Two crews banding overlapping slabs in opposite orders
// would otherwise deadlock.
func bundleByUUID(ctx context.Context, q pgxQuerier, uuid string, forUpdate bool) (bundleRow, error) {
	sql := `SELECT inventory_bundle_id, bundle_code, bundle_status,
	               inventory_item_id, warehouse_id, inventory_bin_id
	        FROM inventory_bundle
	        WHERE inventory_bundle_uuid = $1 AND bundle_deleted_at IS NULL`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var b bundleRow
	err := q.QueryRow(ctx, sql, uuid).Scan(
		&b.id, &b.code, &b.status, &b.itemID, &b.warehouseID, &b.binID)
	if errors.Is(err, pgx.ErrNoRows) {
		return bundleRow{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return bundleRow{}, ErrNotFound
		}
		return bundleRow{}, fmt.Errorf("resolve bundle: %w", err)
	}
	return b, nil
}

// validateBundleStatus keeps a client-supplied status inside chk_bundle_status,
// so a typo is a 400 rather than a 500 from the CHECK.
func validateBundleStatus(status string) error {
	switch status {
	case BundleOpen, BundleSealed, BundleBroken:
		return nil
	}
	return ClientError{Msg: "A bundle status must be open, sealed or broken."}
}

// mapBundleWriteErr turns a constraint violation into a client-facing 400 where
// one is warranted.
func mapBundleWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "A bundle with this code already exists."}
	case isFKViolation(err):
		return ClientError{Msg: "Unknown vendor, item, warehouse or bin."}
	case isCheckViolation(err):
		return ClientError{Msg: "One or more bundle values are out of range."}
	}
	return fmt.Errorf("%s bundle: %w", verb, err)
}

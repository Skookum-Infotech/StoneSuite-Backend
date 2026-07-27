package inventory

// bundle_store_write.go — bundle create, update and delete.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func validateBundleInput(in *BundleInput) error {
	in.Code = strings.TrimSpace(in.Code)
	if in.Code == "" {
		return ClientError{Msg: "A bundle code is required."}
	}
	if len(in.Code) > 50 {
		return ClientError{Msg: "A bundle code cannot exceed 50 characters."}
	}
	if in.WarehouseID <= 0 {
		return ClientError{Msg: "A warehouse is required."}
	}
	// Mirrors chk_bundle_supplier, so the caller gets a sentence instead of a
	// constraint name.
	if strings.TrimSpace(in.SupplierCode) != "" && in.VendorID == nil {
		return ClientError{Msg: "A supplier code needs a vendor."}
	}
	return nil
}

// resolveBundleItem resolves the optional catalogue item a bundle is sawn from.
func resolveBundleItem(ctx context.Context, q pgxQuerier, itemUUID *string) (*int, error) {
	if itemUUID == nil || strings.TrimSpace(*itemUUID) == "" {
		return nil, nil
	}
	info, err := resolveItemForUnit(ctx, q, *itemUUID)
	if err != nil {
		return nil, err
	}
	return &info.itemID, nil
}

// nullableDate turns an optional 'YYYY-MM-DD' string into a nullable parameter.
func nullableDate(s *string) any {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return strings.TrimSpace(*s)
}

// CreateBundle registers a banded pallet, optionally attaching its members in
// the same transaction so receiving one is a single call.
func CreateBundle(ctx context.Context, pool *pgxpool.Pool, in BundleInput, actorEmployeeID int) (*Bundle, error) {
	if err := validateBundleInput(&in); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	itemID, err := resolveBundleItem(ctx, tx, in.InventoryItemUUID)
	if err != nil {
		return nil, err
	}
	binID, err := resolveUnitBin(ctx, tx, in.BinUUID, in.WarehouseID)
	if err != nil {
		return nil, err
	}

	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_bundle (
			bundle_code, bundle_vendor_id, bundle_supplier_code,
			bundle_block_id, bundle_lot, inventory_item_id,
			warehouse_id, inventory_bin_id, bundle_status,
			bundle_received_at, bundle_notes, bundle_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'open',$9,$10,$11)
		RETURNING inventory_bundle_uuid`,
		in.Code, nullableIntPtr(in.VendorID), in.SupplierCode,
		in.BlockID, in.Lot, itemID,
		in.WarehouseID, binID,
		nullableDate(in.ReceivedAt), in.Notes, nullableInt(actorEmployeeID),
	).Scan(&newUUID)
	if err != nil {
		return nil, mapBundleWriteErr(err, "insert")
	}

	if len(in.MemberIDs) > 0 {
		if err := attachMembers(ctx, tx, newUUID, in.MemberIDs, "", actorEmployeeID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create bundle: %w", err)
	}
	return GetBundle(ctx, pool, newUUID)
}

// UpdateBundle edits a bundle's paperwork. It does not touch status or
// membership — those move through Seal, Break and the member endpoints.
//
// A sealed bundle's warehouse and bin are frozen: relocating a banded pallet is
// MoveBundle's job, which carries every member with it. Editing the bundle's
// location alone would leave the members behind and the paperwork lying.
func UpdateBundle(ctx context.Context, pool *pgxpool.Pool, uuid string, in BundleInput, actorEmployeeID int) error {
	if err := validateBundleInput(&in); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := bundleByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	if cur.warehouseID != in.WarehouseID {
		return ClientError{Msg: "A bundle cannot be moved to a different warehouse. Transfer its units instead."}
	}
	// An OMITTED item or bin means "leave it alone", not "clear it" — see the
	// note on BundleInput. Without this, the first PATCH after a bundle adopted
	// its item from a member would try to clear that item, and checkItemChange
	// would then refuse every further edit to an occupied bundle.
	itemID := cur.itemID
	if in.InventoryItemUUID != nil {
		if itemID, err = resolveBundleItem(ctx, tx, in.InventoryItemUUID); err != nil {
			return err
		}
	}
	if err := checkItemChange(ctx, tx, cur, itemID); err != nil {
		return err
	}
	if err := checkCodeChange(ctx, tx, cur, in.Code); err != nil {
		return err
	}
	binID := cur.binID
	if in.BinUUID != nil {
		if binID, err = resolveUnitBin(ctx, tx, in.BinUUID, cur.warehouseID); err != nil {
			return err
		}
	}
	if cur.status == BundleSealed && !sameIntPtr(binID, cur.binID) {
		return ClientError{Msg: "A sealed bundle's bin changes by moving it, so its units move too."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_bundle SET
			bundle_code = $2, bundle_vendor_id = $3, bundle_supplier_code = $4,
			bundle_block_id = $5, bundle_lot = $6, inventory_item_id = $7,
			inventory_bin_id = $8, bundle_received_at = $9, bundle_notes = $10,
			bundle_updated_at = NOW(), bundle_updated_by = $11,
			bundle_record_version = bundle_record_version + 1
		WHERE inventory_bundle_id = $1`,
		cur.id, in.Code, nullableIntPtr(in.VendorID), in.SupplierCode,
		in.BlockID, in.Lot, itemID, binID,
		nullableDate(in.ReceivedAt), in.Notes, nullableInt(actorEmployeeID)); err != nil {
		return mapBundleWriteErr(err, "update")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update bundle: %w", err)
	}
	return nil
}

// checkItemChange refuses to repoint a bundle at a different catalogue item
// while members are still attached.
//
// Members are held to the bundle's item on the way in, so changing it
// afterwards would leave a pallet whose own record disagrees with every slab on
// it — and TotalArea would then be summing two units of measure.
func checkItemChange(ctx context.Context, tx pgx.Tx, cur bundleRow, next *int) error {
	if sameIntPtr(cur.itemID, next) {
		return nil
	}
	n, err := liveMemberCount(ctx, tx, cur.id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ClientError{Msg: "This bundle already holds units. Remove them before changing its item."}
	}
	return nil
}

// checkCodeChange refuses to rename a bundle that has units attached.
//
// The code is stamped onto every member as slab_bundle_id, and that label is
// deliberately left behind when a member is detached, as provenance. Renaming a
// banded bundle would therefore split the tag in two: attached slabs would need
// updating while already-detached ones would keep pointing at a code that no
// longer exists anywhere. Rename before banding, or issue a new bundle.
func checkCodeChange(ctx context.Context, tx pgx.Tx, cur bundleRow, next string) error {
	if strings.EqualFold(cur.code, next) {
		return nil
	}
	var attached int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_slab
		WHERE inventory_bundle_id = $1 AND slab_deleted_at IS NULL`, cur.id).Scan(&attached); err != nil {
		return fmt.Errorf("count bundle members: %w", err)
	}
	if attached > 0 {
		return ClientError{Msg: "A bundle code cannot change once units carry it. Break the bundle, or create a new one."}
	}
	return nil
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// liveMemberCount counts members still physically on the pallet.
func liveMemberCount(ctx context.Context, q pgxQuerier, bundleID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_slab
		WHERE inventory_bundle_id = $1 AND slab_deleted_at IS NULL
		  AND slab_status NOT IN ('consumed','scrapped')`, bundleID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count bundle members: %w", err)
	}
	return n, nil
}

// DeleteBundle soft-deletes a bundle, refusing while units are still attached.
//
// Breaking the bundle is the way to empty it: Break detaches every member and
// leaves the bundle code on each slab as provenance, so the deletion loses no
// history.
func DeleteBundle(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := bundleByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	var attached int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_slab
		WHERE inventory_bundle_id = $1 AND slab_deleted_at IS NULL`, cur.id).Scan(&attached); err != nil {
		return fmt.Errorf("check bundle members: %w", err)
	}
	if attached > 0 {
		return ClientError{Msg: "This bundle still holds units. Break it first."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_bundle SET bundle_deleted_at = NOW(), bundle_deleted_by = $2
		WHERE inventory_bundle_id = $1`, cur.id, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete bundle: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete bundle: %w", err)
	}
	return nil
}

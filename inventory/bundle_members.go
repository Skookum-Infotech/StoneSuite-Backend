package inventory

// bundle_members.go — attaching and detaching bundle members.
// Sealing, breaking and moving a whole bundle live in bundle_lifecycle.go.
//
// Membership is CURRENT membership: inventory_bundle_id points at the pallet a
// slab is on right now. Provenance is the legacy free-text inventory_slab.
// slab_bundle_id, back-filled from bundle_code on attach and deliberately left
// in place on detach — so "this slab came in on bundle B-4412" survives the band
// being cut, which is what a claim against the vendor actually needs.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sortedUnique canonicalises a list of uuids for locking.
//
// Members are locked in this order, always after the bundle itself. Two crews
// banding overlapping slabs in opposite orders would otherwise deadlock, and
// duplicates in one request would write duplicate history rows.
func sortedUnique(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// AddBundleMembers attaches units to an open bundle.
func AddBundleMembers(ctx context.Context, pool *pgxpool.Pool, uuid string, in BundleMemberInput, actorEmployeeID int) (*Bundle, error) {
	if len(in.MemberIDs) == 0 {
		return nil, ClientError{Msg: "No units were supplied."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin add bundle members: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := attachMembers(ctx, tx, uuid, in.MemberIDs, in.Note, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit add bundle members: %w", err)
	}
	return GetBundle(ctx, pool, uuid)
}

// attachMembers is the shared attach path, used by create and by the add
// endpoint. It expects to own the transaction's bundle lock.
func attachMembers(ctx context.Context, tx pgx.Tx, bundleUUID string, memberIDs []string, note string, actorEmployeeID int) error {
	b, err := bundleByUUID(ctx, tx, bundleUUID, true)
	if err != nil {
		return err
	}
	if b.status != BundleOpen {
		return ClientError{Msg: "Only an open bundle accepts units. Break a sealed one first."}
	}

	itemID := b.itemID
	for _, id := range sortedUnique(memberIDs) {
		u, err := unitByUUID(ctx, tx, id, true)
		if err != nil {
			if err == ErrNotFound {
				return ClientError{Msg: "One of the units does not exist."}
			}
			return err
		}
		if u.bundleID != nil && *u.bundleID == b.id {
			continue // already on this pallet; attaching again is a no-op
		}
		if u.bundleID != nil {
			return ClientError{Msg: "A unit is already in another bundle. Remove it from that one first."}
		}
		if u.status == "consumed" || u.status == "scrapped" {
			return ClientError{Msg: "A consumed or scrapped unit cannot be bundled."}
		}
		if u.warehouseID != b.warehouseID {
			return ClientError{Msg: "A unit must be in the bundle's warehouse before it can be bundled."}
		}
		// The first member fixes the bundle's item; the rest are held to it. A
		// pallet is sawn from one block, and without this TotalArea would be
		// summing two different units of measure.
		if itemID == nil {
			item := u.itemID
			itemID = &item
		} else if *itemID != u.itemID {
			return ClientError{Msg: "Every unit in a bundle must be the same inventory item."}
		}

		// A bundle is a physical stack, so its members are wherever it is.
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_slab SET inventory_bundle_id = $2, slab_bundle_id = $3,
			       inventory_bin_id = COALESCE($4, inventory_bin_id),
			       slab_updated_at = NOW(), slab_updated_by = $5
			WHERE inventory_slab_id = $1`,
			u.id, b.id, b.code, b.binID, nullableInt(actorEmployeeID)); err != nil {
			return mapUnitWriteErr(err, "bundle")
		}
		if err := writeUnitHistory(ctx, tx, u.id, "update", "bundleId", "", b.code,
			u.binID, b.binID, nil, note, actorEmployeeID); err != nil {
			return err
		}
	}

	if !sameIntPtr(itemID, b.itemID) {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_bundle SET inventory_item_id = $2, bundle_updated_at = NOW(),
			       bundle_updated_by = $3, bundle_record_version = bundle_record_version + 1
			WHERE inventory_bundle_id = $1`, b.id, itemID, nullableInt(actorEmployeeID)); err != nil {
			return mapBundleWriteErr(err, "adopt item for")
		}
	}
	return nil
}

// RemoveBundleMembers detaches units from an open bundle, leaving each one where
// it physically stands.
func RemoveBundleMembers(ctx context.Context, pool *pgxpool.Pool, uuid string, in BundleMemberInput, actorEmployeeID int) (*Bundle, error) {
	if len(in.MemberIDs) == 0 {
		return nil, ClientError{Msg: "No units were supplied."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin remove bundle members: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	b, err := bundleByUUID(ctx, tx, uuid, true)
	if err != nil {
		return nil, err
	}
	if b.status != BundleOpen {
		return nil, ClientError{Msg: "Units can only be removed from an open bundle. Break a sealed one instead."}
	}
	for _, id := range sortedUnique(in.MemberIDs) {
		u, err := unitByUUID(ctx, tx, id, true)
		if err != nil {
			if err == ErrNotFound {
				return nil, ClientError{Msg: "One of the units does not exist."}
			}
			return nil, err
		}
		if u.bundleID == nil || *u.bundleID != b.id {
			return nil, ClientError{Msg: "A unit is not in this bundle."}
		}
		if err := detachMember(ctx, tx, u.id, b.code, in.Note, actorEmployeeID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit remove bundle members: %w", err)
	}
	return GetBundle(ctx, pool, uuid)
}

// detachMember clears current membership but keeps slab_bundle_id as the
// historical band tag.
func detachMember(ctx context.Context, tx pgx.Tx, unitID int, bundleCode, note string, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET inventory_bundle_id = NULL,
		       slab_updated_at = NOW(), slab_updated_by = $2
		WHERE inventory_slab_id = $1`, unitID, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "unbundle")
	}
	return writeUnitHistory(ctx, tx, unitID, "update", "bundleId", bundleCode, "",
		nil, nil, nil, note, actorEmployeeID)
}

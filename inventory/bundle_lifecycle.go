package inventory

// bundle_lifecycle.go — sealing, breaking and moving a whole bundle.
//
// The status machine is open -> sealed -> broken, and broken is terminal.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SealBundle bands an open bundle. From here its members move together, and
// MoveUnitToBin refuses a single-member move.
func SealBundle(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Bundle, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin seal bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	b, err := bundleByUUID(ctx, tx, uuid, true)
	if err != nil {
		return nil, err
	}
	if b.status != BundleOpen {
		return nil, ClientError{Msg: "Only an open bundle can be sealed."}
	}
	n, err := liveMemberCount(ctx, tx, b.id)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// Sealing an empty pallet would freeze a bundle that accepts no units and
		// has none to release, leaving break as the only way out of a state the
		// yard never intended to enter.
		return nil, ClientError{Msg: "An empty bundle cannot be sealed."}
	}
	if err := setBundleStatus(ctx, tx, b.id, BundleSealed, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit seal bundle: %w", err)
	}
	return GetBundle(ctx, pool, uuid)
}

// BreakBundle cuts the band and releases every member.
//
// Terminal: re-banding the same stone creates a NEW bundle with a new code,
// because the code is the physical tag stapled to the pallet.
func BreakBundle(ctx context.Context, pool *pgxpool.Pool, uuid string, note string, actorEmployeeID int) (*Bundle, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin break bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	b, err := bundleByUUID(ctx, tx, uuid, true)
	if err != nil {
		return nil, err
	}
	if b.status == BundleBroken {
		return nil, ClientError{Msg: "This bundle is already broken."}
	}

	// Lock and detach in a stable order, matching attachMembers.
	rows, err := tx.Query(ctx, `
		SELECT inventory_slab_uuid FROM inventory_slab
		WHERE inventory_bundle_id = $1 AND slab_deleted_at IS NULL
		ORDER BY inventory_slab_uuid`, b.id)
	if err != nil {
		return nil, fmt.Errorf("load bundle members: %w", err)
	}
	var members []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan bundle member: %w", err)
		}
		members = append(members, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load bundle members: %w", err)
	}

	for _, id := range members {
		u, err := unitByUUID(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if err := detachMember(ctx, tx, u.id, b.code, note, actorEmployeeID); err != nil {
			return nil, err
		}
	}
	if err := setBundleStatus(ctx, tx, b.id, BundleBroken, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit break bundle: %w", err)
	}
	return GetBundle(ctx, pool, uuid)
}

func setBundleStatus(ctx context.Context, tx pgx.Tx, bundleID int, status string, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_bundle SET bundle_status = $2, bundle_updated_at = NOW(),
		       bundle_updated_by = $3, bundle_record_version = bundle_record_version + 1
		WHERE inventory_bundle_id = $1`, bundleID, status, nullableInt(actorEmployeeID)); err != nil {
		return mapBundleWriteErr(err, "set status on")
	}
	return nil
}

// MoveBundle relocates a whole bundle and every unit on it to another bin in the
// same warehouse.
//
// Stock-neutral and so writes NO ledger row, exactly like MoveUnitToBin: bins
// locate serialized units, and inventory_stock is keyed on (item, warehouse).
// This is the legitimate way to move a sealed bundle.
func MoveBundle(ctx context.Context, pool *pgxpool.Pool, uuid string, in MoveBundleInput, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin move bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	b, err := bundleByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	if b.status == BundleBroken {
		return ClientError{Msg: "A broken bundle holds no units to move."}
	}
	newBin, err := resolveUnitBin(ctx, tx, in.BinUUID, b.warehouseID)
	if err != nil {
		return err
	}
	if err := checkBinMoveNotFrozen(ctx, tx, b.warehouseID, b.binID, newBin); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_bundle SET inventory_bin_id = $2, bundle_updated_at = NOW(),
		       bundle_updated_by = $3, bundle_record_version = bundle_record_version + 1
		WHERE inventory_bundle_id = $1`, b.id, newBin, nullableInt(actorEmployeeID)); err != nil {
		return mapBundleWriteErr(err, "move")
	}

	rows, err := tx.Query(ctx, `
		SELECT inventory_slab_id, inventory_bin_id FROM inventory_slab
		WHERE inventory_bundle_id = $1 AND slab_deleted_at IS NULL
		  AND slab_status NOT IN ('consumed','scrapped')
		ORDER BY inventory_slab_id
		FOR UPDATE`, b.id)
	if err != nil {
		return fmt.Errorf("load bundle members: %w", err)
	}
	type member struct {
		id    int
		binID *int
	}
	var members []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.id, &m.binID); err != nil {
			rows.Close()
			return fmt.Errorf("scan bundle member: %w", err)
		}
		members = append(members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load bundle members: %w", err)
	}

	for _, m := range members {
		if sameIntPtr(m.binID, newBin) {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_slab SET inventory_bin_id = $2, slab_updated_at = NOW(), slab_updated_by = $3
			WHERE inventory_slab_id = $1`, m.id, newBin, nullableInt(actorEmployeeID)); err != nil {
			return mapUnitWriteErr(err, "move")
		}
		if err := writeUnitHistory(ctx, tx, m.id, "bin_move", "binId",
			binLabel(m.binID), binLabel(newBin), m.binID, newBin, nil, in.Note, actorEmployeeID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move bundle: %w", err)
	}
	return nil
}

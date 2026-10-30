package inventory

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// MoveUnitToBinTx moves a unit and records history within the caller's transaction.
func MoveUnitToBinTx(ctx context.Context, tx pgx.Tx, uuid string, in MoveUnitInput, actorEmployeeID int) error {
	return moveUnitToBinTx(ctx, tx, uuid, in, nil, actorEmployeeID)
}

// MoveUnitForBinTransferTx moves a unit on behalf of a bin transfer document. It
// applies exactly the rules of any other bin move and tags the history row it
// writes with the document, so the unit's and the bin's history link back to it.
func MoveUnitForBinTransferTx(ctx context.Context, tx pgx.Tx, uuid, binUUID, note string, binTransferID, actorEmployeeID int) error {
	return moveUnitToBinTx(ctx, tx, uuid, MoveUnitInput{BinUUID: &binUUID, Note: note}, &binTransferID, actorEmployeeID)
}

// moveUnitToBinTx is the shared body of the two exported movers; binTransferID
// is nil for a move that did not come from a bin transfer.
func moveUnitToBinTx(ctx context.Context, tx pgx.Tx, uuid string, in MoveUnitInput, binTransferID *int, actorEmployeeID int) error {
	u, err := unitByUUID(ctx, tx, uuid, true)
	if err != nil {
		return err
	}
	if u.status == StatusOnOrder {
		return ClientError{Msg: "This unit is still on order and has not arrived, so it cannot be moved to a bin."}
	}
	if u.status == StatusConsumed || u.status == StatusScrapped {
		return ClientError{Msg: "A consumed or scrapped unit cannot be moved."}
	}
	if u.status == StatusInTransit {
		// It is on a truck between two warehouses and holds no bin at all, so a
		// bin move here would file it into a rack it has not reached.
		return ClientError{Msg: "This unit is in transit. Receive its transfer before moving it to a bin."}
	}
	// A sealed bundle is physically banded to a pallet: moving one member means
	// cutting the bands. Cross-row, so no CHECK can express it.
	if u.bundleID != nil {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_id = $1`, *u.bundleID).Scan(&status); err != nil {
			return fmt.Errorf("read bundle status: %w", err)
		}
		if status == "sealed" {
			return ClientError{Msg: "This unit is in a sealed bundle. Move the whole bundle, or break it first."}
		}
	}

	newBin, err := resolveUnitBin(ctx, tx, in.BinUUID, u.warehouseID)
	if err != nil {
		return err
	}
	// BOTH ends are checked against a live cycle count. Guarding only the source
	// would still let stock be moved INTO a frozen bin, which corrupts that
	// bin's count just as thoroughly as moving stock out of it.
	if err := checkBinMoveNotFrozen(ctx, tx, u.warehouseID, u.binID, newBin); err != nil {
		return err
	}
	// A slab that came in without a bin is put away by this move: it is available
	// from the moment it sits in one.
	status := u.status
	if status == StatusReceived && newBin != nil {
		status = StatusAvailable
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET inventory_bin_id = $2, slab_status = $4, slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`, u.id, newBin, nullableInt(actorEmployeeID), status); err != nil {
		return mapUnitWriteErr(err, "move")
	}
	if err := writeUnitMoveHistory(ctx, tx, u.id,
		binLabel(u.binID), binLabel(newBin), u.binID, newBin, in.Note, binTransferID, actorEmployeeID); err != nil {
		return err
	}
	if status != u.status {
		if err := writeUnitHistory(ctx, tx, u.id, "status_change", "status", u.status, status,
			u.binID, newBin, nil, "Put away in a bin.", actorEmployeeID); err != nil {
			return err
		}
	}
	return nil
}

func binLabel(id *int) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%d", *id)
}

// MoveInspectedUnitToWIPTx requires an accepted unit and an active WIP destination.
// Warehouse, bundle and cycle-count guards are shared with normal bin movements.
func MoveInspectedUnitToWIPTx(ctx context.Context, tx pgx.Tx, uuid, binUUID, note string, actorEmployeeID int) error {
	var inspection string
	if err := tx.QueryRow(ctx, `SELECT inspection_status FROM inventory_slab WHERE inventory_slab_uuid=$1 AND slab_deleted_at IS NULL FOR UPDATE`, uuid).Scan(&inspection); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock WIP material: %w", err)
	}
	if inspection != "accepted" {
		return ClientError{Msg: "Inspect and accept this slab before moving it into WIP."}
	}
	var wip bool
	if err := tx.QueryRow(ctx, `SELECT bin_is_wip AND bin_is_active FROM inventory_bin WHERE inventory_bin_uuid=$1 AND bin_deleted_at IS NULL FOR SHARE`, binUUID).Scan(&wip); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClientError{Msg: "Choose an active WIP bin."}
		}
		return fmt.Errorf("load WIP destination: %w", err)
	}
	if !wip {
		return ClientError{Msg: "Choose an active WIP bin."}
	}
	return MoveUnitToBinTx(ctx, tx, uuid, MoveUnitInput{BinUUID: &binUUID, Note: note}, actorEmployeeID)
}

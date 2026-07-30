package inventory

// doc_slab_ops.go — what the Phase 3 documents are allowed to do to a slab.
//
// Each of these keeps three things in step inside one transaction: the slab
// row's own state, its stock effect through the ledger, and its operational
// history. Letting a document package write any one of them directly is how
// they drift apart.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Slab statuses. 'in_transit' exists so a two-legged transfer is honest: stock
// that has left one yard and not yet reached the other is in neither.
const (
	StatusAvailable = "available"
	StatusReserved  = "reserved"
	StatusConsumed  = "consumed"
	StatusScrapped  = "scrapped"
	StatusInTransit = "in_transit"
)

// ShipSlabForTransfer sends one slab out of its warehouse: status to
// in_transit, stock decremented at the source, bin and open-bundle membership
// cleared because it is physically gone from both.
func ShipSlabForTransfer(ctx context.Context, tx pgx.Tx, u UnitRef,
	fromWarehouseID int, src DocSource, actorEmployeeID int) error {
	if u.WarehouseID != fromWarehouseID {
		return ClientError{Msg: fmt.Sprintf("Unit %s is not in the source warehouse.", u.Serial)}
	}
	switch u.Status {
	case StatusInTransit:
		return ClientError{Msg: fmt.Sprintf("Unit %s is already in transit on another transfer.", u.Serial)}
	case StatusConsumed, StatusScrapped:
		return ClientError{Msg: fmt.Sprintf("Unit %s has been consumed or scrapped and cannot be transferred.", u.Serial)}
	case StatusReserved:
		// Committed to a fabrication job at THIS warehouse. Shipping it would
		// leave the job holding a reservation on stone in another building.
		return ClientError{Msg: fmt.Sprintf("Unit %s is reserved for a job. Release the reservation before transferring it.", u.Serial)}
	}
	if err := checkNotSealedBundle(ctx, tx, u, "transferred"); err != nil {
		return err
	}
	if err := checkNotFrozenByCount(ctx, tx, u.WarehouseID, u.BinPath); err != nil {
		return err
	}

	// Clearing the bin here rather than at receive is deliberate: between the
	// two legs the slab is on a truck, and leaving it pointing at a rack in the
	// warehouse it left would show it as still occupying that slot.
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab
		SET slab_status = $2, inventory_bin_id = NULL, inventory_bundle_id = NULL,
		    slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`,
		u.ID, StatusInTransit, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "ship")
	}
	if err := SlabLedgerAndStockFromDoc(ctx, tx, u.ID, u.ItemID, fromWarehouseID,
		EventTransferred, -u.Area, src, actorEmployeeID); err != nil {
		return err
	}
	return writeUnitHistory(ctx, tx, u.ID, "warehouse_move", "status",
		u.Status, StatusInTransit, u.BinID, nil, nil, "", actorEmployeeID)
}

// ReceiveSlabForTransfer lands a slab at its destination: warehouse and bin
// rewritten, status back to available, stock incremented at the destination.
func ReceiveSlabForTransfer(ctx context.Context, tx pgx.Tx, u UnitRef,
	toWarehouseID int, toBinID *int, src DocSource, actorEmployeeID int) error {
	if u.Status != StatusInTransit {
		return ClientError{Msg: fmt.Sprintf("Unit %s is not in transit, so it cannot be received.", u.Serial)}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab
		SET warehouse_id = $2, inventory_bin_id = $3, slab_status = $4,
		    slab_updated_at = NOW(), slab_updated_by = $5
		WHERE inventory_slab_id = $1`,
		u.ID, toWarehouseID, toBinID, StatusAvailable, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "receive")
	}
	if err := SlabLedgerAndStockFromDoc(ctx, tx, u.ID, u.ItemID, toWarehouseID,
		EventTransferred, u.Area, src, actorEmployeeID); err != nil {
		return err
	}
	return writeUnitHistory(ctx, tx, u.ID, "warehouse_move", "warehouseId",
		fmt.Sprintf("%d", u.WarehouseID), fmt.Sprintf("%d", toWarehouseID),
		nil, toBinID, nil, "", actorEmployeeID)
}

// AdjustSlabDown writes one slab off through an adjustment or count: it becomes
// scrapped and its area leaves stock.
func AdjustSlabDown(ctx context.Context, tx pgx.Tx, u UnitRef,
	reasonID *int, note string, src DocSource, actorEmployeeID int) (float64, error) {
	switch u.Status {
	case StatusScrapped:
		return 0, ClientError{Msg: fmt.Sprintf("Unit %s is already written off.", u.Serial)}
	case StatusConsumed:
		return 0, ClientError{Msg: fmt.Sprintf("Unit %s was consumed and has already left stock.", u.Serial)}
	case StatusInTransit:
		return 0, ClientError{Msg: fmt.Sprintf("Unit %s is in transit. Receive or cancel the transfer first.", u.Serial)}
	}
	if u.Status == StatusReserved {
		// Same rule as ScrapUnit: a written-off unit holds no reservation, or a
		// later consume deducts stock for stone that is already gone.
		if _, err := tx.Exec(ctx, `
			UPDATE fabrication_job_slab SET allocation_status = 'released'
			WHERE inventory_slab_id = $1 AND allocation_status = 'reserved'`, u.ID); err != nil {
			return 0, fmt.Errorf("release reservation on adjustment: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = $2, slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`, u.ID, StatusScrapped, nullableInt(actorEmployeeID)); err != nil {
		return 0, mapUnitWriteErr(err, "adjust")
	}
	if err := SlabLedgerAndStockFromDoc(ctx, tx, u.ID, u.ItemID, u.WarehouseID,
		EventAdjusted, -u.Area, src, actorEmployeeID); err != nil {
		return 0, err
	}
	if err := writeUnitHistory(ctx, tx, u.ID, "status_change", "status",
		u.Status, StatusScrapped, u.BinID, u.BinID, reasonID, note, actorEmployeeID); err != nil {
		return 0, err
	}
	return -u.Area, nil
}

// AdjustSlabUp restores a written-off slab that turned up after all.
//
// Only a previously scrapped unit can come back this way. Stone the system has
// never seen has no serial, dimensions or lot to adjust INTO existence — that
// is a receipt, not an adjustment, and routing it here would mint a unit with
// no provenance.
func AdjustSlabUp(ctx context.Context, tx pgx.Tx, u UnitRef,
	reasonID *int, note string, src DocSource, actorEmployeeID int) (float64, error) {
	if u.Status != StatusScrapped {
		return 0, ClientError{Msg: fmt.Sprintf(
			"Unit %s is already in stock. Only a written-off unit can be adjusted back in; receive genuinely new stone instead.", u.Serial)}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = $2, slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`, u.ID, StatusAvailable, nullableInt(actorEmployeeID)); err != nil {
		return 0, mapUnitWriteErr(err, "adjust")
	}
	if err := SlabLedgerAndStockFromDoc(ctx, tx, u.ID, u.ItemID, u.WarehouseID,
		EventAdjusted, u.Area, src, actorEmployeeID); err != nil {
		return 0, err
	}
	if err := writeUnitHistory(ctx, tx, u.ID, "status_change", "status",
		StatusScrapped, StatusAvailable, u.BinID, u.BinID, reasonID, note, actorEmployeeID); err != nil {
		return 0, err
	}
	return u.Area, nil
}

// checkNotSealedBundle refuses to move a single member of a banded pallet.
// Cross-row, so no CHECK can express it — the same guard MoveUnitToBin and
// CutUnit apply.
func checkNotSealedBundle(ctx context.Context, tx pgx.Tx, u UnitRef, verb string) error {
	if u.BundleID == nil {
		return nil
	}
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_id = $1`,
		*u.BundleID).Scan(&status); err != nil {
		return fmt.Errorf("read bundle status: %w", err)
	}
	if status == BundleSealed {
		return ClientError{Msg: fmt.Sprintf(
			"Unit %s is in a sealed bundle and cannot be %s on its own. Break the bundle first.", u.Serial, verb)}
	}
	return nil
}

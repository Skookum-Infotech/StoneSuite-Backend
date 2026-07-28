package inventorytransfer

// store_legs.go — the transition endpoint and the two legs that move stock.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
	"stonesuite-backend/inventory"
)

// Transition moves the document along its status graph.
//
// The two moves that touch stock — APPV -> TRNS and TRNS -> RCVD — are refused
// here and routed to Ship and Receive. A bare status write into TRNS would mark
// the stone as gone from the source without ever deducting it.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode, note string, actorEmployeeID int) (*Transfer, error) {
	switch toStatusCode {
	case StatusInTransit:
		return nil, ClientError{Msg: "Use the ship endpoint to send a transfer."}
	case StatusReceived:
		return nil, ClientError{Msg: "Use the receive endpoint to land a transfer."}
	}
	if !machine.Known(toStatusCode) {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if err := machine.Validate(cur.statusCode, toStatusCode); err != nil {
		if toStatusCode == StatusCancelled && HasShipped(cur.statusCode) {
			// Spelled out because "invalid transition" would send the user looking
			// for a permission problem. See the note on machine.
			return nil, ClientError{Msg: "This transfer has already shipped and cannot be cancelled. Receive it, then transfer it back."}
		}
		return nil, ClientError{Msg: fmt.Sprintf("A transfer cannot go from %s to %s.", cur.statusCode, toStatusCode)}
	}

	toStatusID, recordTypeID, err := statusFor(ctx, tx, toStatusCode)
	if err != nil {
		return nil, err
	}
	_ = recordTypeID

	set := `transfer_status = $2, transfer_updated_at = NOW(), transfer_updated_by = $3,
	        transfer_record_version = transfer_record_version + 1`
	args := []any{cur.id, toStatusID, nullableInt(actorEmployeeID)}
	if toStatusCode == StatusCancelled {
		set += `, transfer_cancelled_at = NOW(), transfer_cancelled_by = $3, transfer_cancel_reason = $4`
		// chk_itrf_cancel_pair needs both halves.
		args[2] = actorOrSystem(actorEmployeeID)
		args = append(args, note)
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory_transfer SET `+set+`
		WHERE inventory_transfer_id = $1`, args...); err != nil {
		return nil, mapWriteErr(err, "transition")
	}

	action := "transition"
	if toStatusCode == StatusApproved {
		action = "approve"
	}
	if err := writeHistory(ctx, tx, cur.id, action, &cur.statusID, &toStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

func statusFor(ctx context.Context, tx pgx.Tx, code string) (statusID, recordTypeID int, err error) {
	recordTypeID, err = docflow.RecordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return 0, 0, err
	}
	statusID, err = docflow.StatusIDByCode(ctx, tx, recordTypeID, code)
	if err != nil {
		return 0, 0, ClientError{Msg: "Unknown target status."}
	}
	return statusID, recordTypeID, nil
}

// Ship sends an approved transfer: stock leaves the source warehouse.
func Ship(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Transfer, error) {
	return move(ctx, pool, uuid, legShip, actorEmployeeID)
}

// Receive lands an in-transit transfer: stock arrives at the destination.
func Receive(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Transfer, error) {
	return move(ctx, pool, uuid, legReceive, actorEmployeeID)
}

type leg int

const (
	legShip leg = iota
	legReceive
)

// move runs one leg of the transfer. Both legs are the same shape — validate
// the status, walk the lines, write the header stamp — so they share a body
// rather than drifting apart as two near-copies.
func move(ctx context.Context, pool *pgxpool.Pool, uuid string, which leg, actorEmployeeID int) (*Transfer, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transfer leg: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	toCode := StatusInTransit
	if which == legReceive {
		toCode = StatusReceived
	}
	if err := machine.Validate(cur.statusCode, toCode); err != nil {
		if which == legShip {
			return nil, ClientError{Msg: "Only an approved transfer can be shipped."}
		}
		return nil, ClientError{Msg: "Only a transfer that is in transit can be received."}
	}

	toStatusID, recordTypeID, err := statusFor(ctx, tx, toCode)
	if err != nil {
		return nil, err
	}
	lines, err := postableLines(ctx, tx, cur.id)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ClientError{Msg: "This transfer has no lines."}
	}

	for _, l := range lines {
		src := inventory.DocSource{RecordTypeID: recordTypeID, RecordID: cur.id, LineID: l.id}
		if which == legShip {
			err = shipLine(ctx, tx, l, cur, src, actorEmployeeID)
		} else {
			err = receiveLine(ctx, tx, l, cur, src, actorEmployeeID)
		}
		if err != nil {
			return nil, err
		}
	}

	stamp := `transfer_shipped_at = NOW(), transfer_shipped_by = $3`
	if which == legReceive {
		stamp = `transfer_received_at = NOW(), transfer_received_by = $3`
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_transfer SET transfer_status = $2, `+stamp+`,
			transfer_updated_at = NOW(), transfer_updated_by = $3,
			transfer_record_version = transfer_record_version + 1
		WHERE inventory_transfer_id = $1`,
		cur.id, toStatusID, actorOrSystem(actorEmployeeID)); err != nil {
		return nil, mapWriteErr(err, "stamp")
	}
	action := "ship"
	if which == legReceive {
		action = "receive"
	}
	if err := writeHistory(ctx, tx, cur.id, action, &cur.statusID, &toStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transfer leg: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// shipLine deducts one line from the source warehouse.
func shipLine(ctx context.Context, tx pgx.Tx, l postableLine, cur transferRow,
	src inventory.DocSource, actorEmployeeID int) error {
	if l.slabUUID == nil {
		if err := inventory.CheckNotFrozenByCount(ctx, tx, cur.fromWHID, ""); err != nil {
			return err
		}
		err := inventory.LedgerAndStockFromDoc(ctx, tx, l.itemID, cur.fromWHID,
			inventory.EventTransferred, -l.qty, src, actorEmployeeID)
		if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
			return ClientError{Msg: "This transfer has already been shipped."}
		}
		return err
	}
	// Re-read under lock. The draft-time validation may be days old, and this is
	// also what serialises two crews trying to ship the same slab: the second
	// blocks here, then finds the status already in_transit and is refused.
	unit, err := inventory.ResolveUnitForDocument(ctx, tx, *l.slabUUID, true)
	if err != nil {
		if err == inventory.ErrNotFound {
			return ClientError{Msg: fmt.Sprintf("Unit %s no longer exists.", l.serial)}
		}
		return err
	}
	err = inventory.ShipSlabForTransfer(ctx, tx, unit, cur.fromWHID, src, actorEmployeeID)
	if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
		return ClientError{Msg: fmt.Sprintf("Unit %s has already been shipped on this transfer.", unit.Serial)}
	}
	return err
}

// receiveLine lands one line at the destination warehouse.
func receiveLine(ctx context.Context, tx pgx.Tx, l postableLine, cur transferRow,
	src inventory.DocSource, actorEmployeeID int) error {
	if l.slabUUID == nil {
		err := inventory.LedgerAndStockFromDoc(ctx, tx, l.itemID, cur.toWHID,
			inventory.EventTransferred, l.qty, src, actorEmployeeID)
		if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
			return ClientError{Msg: "This transfer has already been received."}
		}
		return err
	}
	unit, err := inventory.ResolveUnitForDocument(ctx, tx, *l.slabUUID, true)
	if err != nil {
		if err == inventory.ErrNotFound {
			return ClientError{Msg: fmt.Sprintf("Unit %s no longer exists.", l.serial)}
		}
		return err
	}
	err = inventory.ReceiveSlabForTransfer(ctx, tx, unit, cur.toWHID, cur.toBinID, src, actorEmployeeID)
	if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
		return ClientError{Msg: fmt.Sprintf("Unit %s has already been received on this transfer.", unit.Serial)}
	}
	return err
}

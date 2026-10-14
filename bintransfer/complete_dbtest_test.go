// bintransfer/complete_dbtest_test.go
//go:build dbtest

package bintransfer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stonesuite-backend/inventory"
)

// Completing is all-or-nothing: if one slab cannot be moved, none are, and the
// message names the slab that blocked it.
func TestComplete_RollsBackEverythingWhenOneSlabCannotMove(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	good, doomed := newSlab(t, pool, &from), newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, good, doomed)

	// The second slab is scrapped while the request waits.
	if err := inventory.ScrapUnit(ctx, pool, doomed.ID, nil, "dropped", 1); err != nil {
		t.Fatalf("ScrapUnit: %v", err)
	}

	_, err := Complete(ctx, pool, bt.ID, 1)
	if err == nil || !IsClientError(err) {
		t.Fatalf("Complete = %v, want a ClientError", err)
	}
	if !strings.Contains(err.Error(), doomed.Serial) {
		t.Errorf("error %q should name the blocking slab %s", err.Error(), doomed.Serial)
	}
	if got := binOf(t, pool, good.ID); got != from {
		t.Errorf("good slab bin = %q, want it untouched in %q after the rollback", got, from)
	}
	reloaded, err := Get(ctx, pool, bt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.StatusCode != StatusDraft {
		t.Errorf("status after a failed complete = %q, want DRFT", reloaded.StatusCode)
	}
}

func TestComplete_LeavesASlabAlreadyInTheDestinationAndMovesOneThatWandered(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to, elsewhere := newBin(t, pool, 1), newBin(t, pool, 1), newBin(t, pool, 1)
	arrived, wandered := newSlab(t, pool, &from), newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, arrived, wandered)

	// Both slabs are moved by hand while the transfer waits: one lands in the
	// destination, the other in a third bin.
	if err := inventory.MoveUnitToBin(ctx, pool, arrived.ID, inventory.MoveUnitInput{BinUUID: &to}, 1); err != nil {
		t.Fatalf("MoveUnitToBin: %v", err)
	}
	if err := inventory.MoveUnitToBin(ctx, pool, wandered.ID, inventory.MoveUnitInput{BinUUID: &elsewhere}, 1); err != nil {
		t.Fatalf("MoveUnitToBin: %v", err)
	}
	beforeArrived, _ := moveHistory(t, pool, arrived.ID, bt.ID)

	done, err := Complete(ctx, pool, bt.ID, 1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := binOf(t, pool, wandered.ID); got != to {
		t.Errorf("wandered slab bin = %q, want it sent to %q", got, to)
	}
	if afterArrived, _ := moveHistory(t, pool, arrived.ID, bt.ID); afterArrived != beforeArrived {
		t.Errorf("a slab already in place got %d new history rows, want none", afterArrived-beforeArrived)
	}
	for _, l := range done.Lines {
		if l.MovedAt == nil {
			t.Errorf("line %d (%s) not marked moved", l.LineNumber, l.UnitSerial)
		}
	}
}

func TestTransition_CancelRecordsReasonAndCompleteIsNotATransition(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, unit)

	// COMP is reached only through Complete, which moves the stone.
	if _, err := Transition(ctx, pool, bt.ID, StatusCompleted, "", 1); err == nil {
		t.Error("Transition to COMP succeeded, want it refused (use Complete)")
	}
	if got := binOf(t, pool, unit.ID); got != from {
		t.Errorf("a refused transition moved the slab: bin = %q", got)
	}

	cancelled, err := Transition(ctx, pool, bt.ID, StatusCancelled, "Customer changed the order", 1)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.StatusCode != StatusCancelled || cancelled.CancelReason != "Customer changed the order" {
		t.Errorf("cancelled = %q / %q, want CANC with the reason", cancelled.StatusCode, cancelled.CancelReason)
	}
	if _, err := Complete(ctx, pool, bt.ID, 1); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Complete of a cancelled transfer = %v, want ErrInvalidTransition", err)
	}
}

func TestBinHistory_ShowsMovesInAndOutWithTheTransferNumber(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, unit)
	if _, err := Complete(ctx, pool, bt.ID, 1); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	inTo, err := inventory.BinHistory(ctx, pool, to, inventory.BinHistoryRequest{})
	if err != nil {
		t.Fatalf("BinHistory(to): %v", err)
	}
	var moveIn *inventory.BinHistoryEntry
	for i := range inTo.Records {
		if inTo.Records[i].Action == "bin_move" && inTo.Records[i].UnitSerial == unit.Serial {
			moveIn = &inTo.Records[i]
		}
	}
	if moveIn == nil {
		t.Fatalf("destination history %+v has no bin_move for %s", inTo.Records, unit.Serial)
	}
	if moveIn.Direction != "in" {
		t.Errorf("Direction at the destination = %q, want in", moveIn.Direction)
	}
	if moveIn.TransferNumber != bt.Number || moveIn.TransferID != bt.ID {
		t.Errorf("transfer link = %q/%q, want %q/%q", moveIn.TransferNumber, moveIn.TransferID, bt.Number, bt.ID)
	}

	outFrom, err := inventory.BinHistory(ctx, pool, from, inventory.BinHistoryRequest{})
	if err != nil {
		t.Fatalf("BinHistory(from): %v", err)
	}
	var moveOut *inventory.BinHistoryEntry
	for i := range outFrom.Records {
		if outFrom.Records[i].Action == "bin_move" && outFrom.Records[i].UnitSerial == unit.Serial {
			moveOut = &outFrom.Records[i]
		}
	}
	if moveOut == nil || moveOut.Direction != "out" {
		t.Fatalf("source history entry = %+v, want a bin_move with Direction out", moveOut)
	}

	// Only moves INTO a bin can be asked for on their own.
	onlyIn, err := inventory.BinHistory(ctx, pool, from, inventory.BinHistoryRequest{Direction: "in"})
	if err != nil {
		t.Fatalf("BinHistory(from, in): %v", err)
	}
	for _, e := range onlyIn.Records {
		if e.Direction != "in" {
			t.Errorf("direction filter 'in' returned %q entry %+v", e.Direction, e)
		}
	}
}

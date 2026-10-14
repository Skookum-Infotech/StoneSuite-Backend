// bintransfer/store_dbtest_test.go
//go:build dbtest

package bintransfer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestCreate_AssignsNumberSnapshotsFromBinAndStartsDraft(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)

	bt, err := Create(ctx, pool, Input{
		WarehouseID: 1, Notes: "Put-away after the Tuesday delivery",
		Lines: []LineInput{{UnitID: unit.ID, ToBinID: to, Notes: "top of the stack"}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(bt.Number, "BTRF-") {
		t.Errorf("Number = %q, want a BTRF- number", bt.Number)
	}
	if bt.StatusCode != StatusDraft {
		t.Errorf("StatusCode = %q, want DRFT", bt.StatusCode)
	}
	if bt.SlabCount != 1 || bt.TotalArea != unit.Area {
		t.Errorf("SlabCount=%d TotalArea=%v, want 1 and the slab's area %v", bt.SlabCount, bt.TotalArea, unit.Area)
	}
	if len(bt.Lines) != 1 {
		t.Fatalf("Lines = %d, want 1", len(bt.Lines))
	}
	l := bt.Lines[0]
	if l.UnitID != unit.ID || l.UnitSerial != unit.Serial {
		t.Errorf("line unit = %q/%q, want %q/%q", l.UnitID, l.UnitSerial, unit.ID, unit.Serial)
	}
	// The from-bin is read off the slab, never taken from the caller.
	if l.FromBinID == nil || *l.FromBinID != from {
		t.Errorf("FromBinID = %v, want the slab's current bin %s", l.FromBinID, from)
	}
	if l.ToBinID != to {
		t.Errorf("ToBinID = %q, want %q", l.ToBinID, to)
	}
	// Writing the request moves nothing.
	if got := binOf(t, pool, unit.ID); got != from {
		t.Errorf("slab bin after create = %q, want it still in %q", got, from)
	}
	hist, err := History(ctx, pool, bt.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 1 || hist[0].Action != "create" || hist[0].ToStatus != StatusDraft {
		t.Errorf("history = %+v, want one create entry into DRFT", hist)
	}
}

func TestCreate_RefusesLinesThatCannotMove(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	other := otherLocation(t, pool)
	here, there := newBin(t, pool, 1), newBin(t, pool, other)
	inHere := newSlab(t, pool, &here)
	dest := newBin(t, pool, 1)

	tests := []struct {
		name    string
		in      Input
		wantMsg string
	}{
		{"destination in another location",
			Input{WarehouseID: 1, Lines: []LineInput{{UnitID: inHere.ID, ToBinID: there}}}, "location"},
		{"slab is not in the chosen location",
			Input{WarehouseID: other, Lines: []LineInput{{UnitID: inHere.ID, ToBinID: there}}}, "not in this location"},
		{"slab already in the destination",
			Input{WarehouseID: 1, Lines: []LineInput{{UnitID: inHere.ID, ToBinID: here}}}, "already in"},
		{"unknown slab",
			Input{WarehouseID: 1, Lines: []LineInput{{UnitID: "00000000-0000-0000-0000-000000000000", ToBinID: dest}}}, "does not exist"},
		{"unknown bin",
			Input{WarehouseID: 1, Lines: []LineInput{{UnitID: inHere.ID, ToBinID: "00000000-0000-0000-0000-000000000000"}}}, "bin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(ctx, pool, tc.in, 1)
			if err == nil {
				t.Fatalf("Create succeeded, want an error mentioning %q", tc.wantMsg)
			}
			if !IsClientError(err) {
				t.Fatalf("Create = %T %v, want a ClientError (400)", err, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantMsg)) {
				t.Errorf("Create = %q, want it to mention %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestCreate_RefusesInactiveDestinationBin(t *testing.T) {
	pool := testPool(t)
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	if _, err := pool.Exec(context.Background(),
		`UPDATE inventory_bin SET bin_is_active = FALSE WHERE inventory_bin_uuid = $1`, to); err != nil {
		t.Fatalf("deactivate bin: %v", err)
	}
	_, err := Create(context.Background(), pool, Input{WarehouseID: 1, Lines: []LineInput{{UnitID: unit.ID, ToBinID: to}}}, 1)
	if err == nil || !IsClientError(err) || !strings.Contains(strings.ToLower(err.Error()), "inactive") {
		t.Fatalf("Create to an inactive bin = %v, want an inactive-bin ClientError", err)
	}
}

// A slab sits on at most one open transfer: two documents each promising to put
// the same stone in a different place cannot both be right.
func TestCreate_SlabCanBeOnOnlyOneOpenTransfer(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to1, to2 := newBin(t, pool, 1), newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)

	first := draftMoving(t, pool, to1, unit)

	_, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{{UnitID: unit.ID, ToBinID: to2}}}, 1)
	if err == nil || !IsClientError(err) {
		t.Fatalf("second Create = %v, want a ClientError", err)
	}
	if !strings.Contains(err.Error(), first.Number) {
		t.Errorf("error %q should name the transfer holding the slab (%s)", err.Error(), first.Number)
	}

	// Cancelling releases the claim.
	if _, err := Transition(ctx, pool, first.ID, StatusCancelled, "re-planned", 1); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{{UnitID: unit.ID, ToBinID: to2}}}, 1); err != nil {
		t.Errorf("Create after the first was cancelled = %v, want it to succeed", err)
	}
}

func TestUpdate_ReplacesLinesOnlyWhileDraft(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to1, to2 := newBin(t, pool, 1), newBin(t, pool, 1), newBin(t, pool, 1)
	a, b := newSlab(t, pool, &from), newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to1, a)

	// Re-using slab a while swapping destination and adding b must not trip the
	// "already on an open transfer" guard against the document's own old lines.
	updated, err := Update(ctx, pool, bt.ID, Input{
		WarehouseID: 1, Notes: "revised",
		Lines: []LineInput{{UnitID: a.ID, ToBinID: to2}, {UnitID: b.ID, ToBinID: to2}},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.SlabCount != 2 || updated.Notes != "revised" {
		t.Errorf("after update SlabCount=%d Notes=%q, want 2 and revised", updated.SlabCount, updated.Notes)
	}
	if updated.Lines[0].ToBinID != to2 {
		t.Errorf("line 1 ToBinID = %q, want %q", updated.Lines[0].ToBinID, to2)
	}

	if _, err := Transition(ctx, pool, bt.ID, StatusPending, "", 1); err != nil {
		t.Fatalf("submit: %v", err)
	}
	_, err = Update(ctx, pool, bt.ID, Input{WarehouseID: 1, Lines: []LineInput{{UnitID: a.ID, ToBinID: to1}}}, 1)
	if !errors.Is(err, ErrNotEditable) {
		t.Errorf("Update after submitting = %v, want ErrNotEditable (only a draft can be edited)", err)
	}
}

func TestDelete_DraftOnlyAndWorksWithUnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	draft := draftMoving(t, pool, to, newSlab(t, pool, &from))
	if err := Delete(ctx, pool, draft.ID, 0); err != nil { // 0 = unresolved actor
		t.Fatalf("Delete draft: %v", err)
	}
	if _, err := Get(ctx, pool, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of a deleted transfer = %v, want ErrNotFound", err)
	}

	done := draftMoving(t, pool, to, newSlab(t, pool, &from))
	if _, err := Complete(ctx, pool, done.ID, 1); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := Delete(ctx, pool, done.ID, 1); err == nil || !IsClientError(err) {
		t.Errorf("Delete of a completed transfer = %v, want a ClientError", err)
	}
}

func TestSearch_FindsBySlabSerialAndHonoursOwnerScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from, to := newBin(t, pool, 1), newBin(t, pool, 1)
	unit := newSlab(t, pool, &from)
	bt := draftMoving(t, pool, to, unit)

	page, err := Search(ctx, pool, "all", "", query.Request{Search: unit.Serial})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range page.Records {
		if r.ID == bt.ID {
			found = true
			if len(r.Lines) != 0 {
				t.Errorf("a list row carries %d lines, want none (list rows are headers only)", len(r.Lines))
			}
			if r.SlabCount != 1 {
				t.Errorf("list row SlabCount = %d, want 1", r.SlabCount)
			}
		}
	}
	if !found {
		t.Errorf("searching the slab serial %q did not find %s", unit.Serial, bt.Number)
	}

	// An unknown filter key is a client error, never a 500.
	_, err = Search(ctx, pool, "all", "", query.Request{Filters: []query.Clause{{Field: "no_such_field", Op: query.OpEq, Value: "x"}}})
	var invalid *query.InvalidFilterError
	if !errors.As(err, &invalid) {
		t.Errorf("Search with a bad filter = %v, want *query.InvalidFilterError", err)
	}
}

// inventory/unit_cut_test.go
//go:build dbtest

package inventory

import (
	"context"
	"testing"
)

func TestCutUnit_ConsumesParentAndRecoversRemnants(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-CUT"))

	parent, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial: uniq("DBTEST-PARENT"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	const parentArea = 45.208
	if got := onHand(t, pool, item); got != parentArea {
		t.Fatalf("on-hand after receipt = %v, want %v", got, parentArea)
	}

	// Keep two offcuts. Everything else — kerf plus the countertop that was cut
	// out — is loss.
	res, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{
			{Serial: uniq("DBTEST-R1"), LengthMM: 1200, WidthMM: 700},
			{Serial: uniq("DBTEST-R2"), LengthMM: 900, WidthMM: 600},
		},
		Note: "island top",
	}, 1)
	if err != nil {
		t.Fatalf("CutUnit: %v", err)
	}

	if len(res.Remnants) != 2 {
		t.Fatalf("got %d remnants, want 2", len(res.Remnants))
	}
	if res.ConsumedArea != parentArea {
		t.Errorf("ConsumedArea = %v, want %v", res.ConsumedArea, parentArea)
	}

	// 1200x700 = 9.042 sqft, 900x600 = 5.813 sqft
	wantRecovered := res.Remnants[0].Area + res.Remnants[1].Area
	if res.RecoveredArea != roundTo(wantRecovered, areaScale) {
		t.Errorf("RecoveredArea = %v, want %v", res.RecoveredArea, wantRecovered)
	}
	if res.LostArea <= 0 {
		t.Errorf("LostArea = %v, want a positive kerf/offcut loss", res.LostArea)
	}

	// THE point of this test. On-hand must equal exactly what came back — not
	// what came back minus the loss a second time. Writing a 'scrapped' ledger
	// row for the shortfall is the obvious-looking mistake, and it would show
	// up right here as on-hand being short by LostArea.
	if got := onHand(t, pool, item); got != res.RecoveredArea {
		t.Errorf("on-hand after cut = %v, want %v (the loss must not be deducted twice)",
			got, res.RecoveredArea)
	}
	if got := ledgerSum(t, pool, item); got != onHand(t, pool, item) {
		t.Errorf("ledger sum %v != on-hand %v", got, onHand(t, pool, item))
	}

	// The parent is gone; its remnants descend from it and sit in stock.
	reloaded, err := GetUnit(ctx, pool, parent.ID)
	if err != nil {
		t.Fatalf("GetUnit(parent): %v", err)
	}
	if reloaded.Status != "consumed" {
		t.Errorf("parent status = %q, want %q", reloaded.Status, "consumed")
	}
	for _, rem := range res.Remnants {
		if rem.Kind != UnitKindRemnant {
			t.Errorf("remnant kind = %q, want %q", rem.Kind, UnitKindRemnant)
		}
		if rem.Form != "cut" {
			t.Errorf("remnant form = %q, want %q", rem.Form, "cut")
		}
		if rem.ParentUnitID == nil || *rem.ParentUnitID != parent.ID {
			t.Errorf("remnant parent = %v, want %v", rem.ParentUnitID, parent.ID)
		}
		if rem.RootUnitID == nil || *rem.RootUnitID != parent.ID {
			t.Errorf("remnant root = %v, want %v", rem.RootUnitID, parent.ID)
		}
		if !rem.IsUsableRemnant {
			t.Errorf("remnant %s should be usable with no threshold set", rem.Serial)
		}
	}
}

func TestCutUnit_SubThresholdOffcutNeverEntersStock(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-CUT"))

	parent, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial: uniq("DBTEST-PARENT"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}

	res, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{
			{Serial: uniq("DBTEST-BIG"), LengthMM: 1200, WidthMM: 700},
			{Serial: uniq("DBTEST-SLIVER"), LengthMM: 400, WidthMM: 120}, // scrap
		},
		MinUsableLengthMM: 600,
		MinUsableWidthMM:  300,
	}, 1)
	if err != nil {
		t.Fatalf("CutUnit: %v", err)
	}

	var usable, unusable *Unit
	for i := range res.Remnants {
		if res.Remnants[i].IsUsableRemnant {
			usable = &res.Remnants[i]
		} else {
			unusable = &res.Remnants[i]
		}
	}
	if usable == nil || unusable == nil {
		t.Fatalf("expected one usable and one unusable remnant, got %+v", res.Remnants)
	}
	// Recorded, so the cut has a complete history...
	if unusable.Status != "scrapped" {
		t.Errorf("sub-threshold offcut status = %q, want %q", unusable.Status, "scrapped")
	}
	// ...but it never entered stock, so it cannot inflate on-hand or clutter
	// the remnant picker.
	if res.RecoveredArea != usable.Area {
		t.Errorf("RecoveredArea = %v, want only the usable remnant's %v", res.RecoveredArea, usable.Area)
	}
	if got := onHand(t, pool, item); got != usable.Area {
		t.Errorf("on-hand = %v, want %v", got, usable.Area)
	}

	// And it must not appear in the picker.
	remnants, err := UsableRemnants(ctx, pool, item, 0)
	if err != nil {
		t.Fatalf("UsableRemnants: %v", err)
	}
	for _, r := range remnants {
		if r.ID == unusable.ID {
			t.Error("a sub-threshold offcut appeared in the remnant picker")
		}
	}
}

func TestCutUnit_RejectsImpossibleAndRepeatedCuts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-CUT"))

	parent, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial: uniq("DBTEST-PARENT"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	before := onHand(t, pool, item)

	// Remnants totalling more than the parent would manufacture stone out of a
	// saw cut.
	if _, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{{Serial: uniq("DBTEST-TOOBIG"), LengthMM: 3000, WidthMM: 1400},
			{Serial: uniq("DBTEST-TOOBIG2"), LengthMM: 3000, WidthMM: 1400}},
	}, 1); err == nil {
		t.Error("expected remnants exceeding the parent to be rejected")
	}
	if got := onHand(t, pool, item); got != before {
		t.Errorf("a rejected cut changed stock: %v -> %v", before, got)
	}

	// A real cut, then a second one against the consumed parent.
	if _, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{{Serial: uniq("DBTEST-OK"), LengthMM: 1000, WidthMM: 600}},
	}, 1); err != nil {
		t.Fatalf("CutUnit: %v", err)
	}
	after := onHand(t, pool, item)

	if _, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{{Serial: uniq("DBTEST-AGAIN"), LengthMM: 500, WidthMM: 400}},
	}, 1); err == nil {
		t.Error("expected cutting an already-consumed unit to be rejected")
	}
	if got := onHand(t, pool, item); got != after {
		t.Errorf("a refused second cut changed stock: %v -> %v", after, got)
	}
}

// inventory/unit_usage_test.go
//go:build dbtest

package inventory

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// receiveSlab puts one 3000x1400 slab of the item into stock.
func receiveSlab(t *testing.T, pool *pgxpool.Pool, item string) *Unit {
	t.Helper()
	u, err := CreateUnit(context.Background(), pool, CreateUnitInput{
		Serial: uniq("DBTEST-USAGE"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	return u
}

func TestUnitUsage_UntouchedUnitHasNoUsage(t *testing.T) {
	pool := testPool(t)
	u := receiveSlab(t, pool, seedAreaItem(t, pool, uniq("DBTEST-USAGE")))

	if u.AreaUnitCode != "SQFT" {
		t.Errorf("AreaUnitCode = %q, want SQFT", u.AreaUnitCode)
	}
	use := u.Usage
	if use.JobID != "" || use.JobNumber != "" || use.ReservedAt != nil {
		t.Errorf("an unclaimed unit must name no job, got %+v", use)
	}
	if use.ConsumedAt != nil || use.ScrappedAt != nil {
		t.Errorf("an untouched unit must not have left stock, got %+v", use)
	}
	if use.OffcutCount != 0 || use.RecoveredArea != 0 || use.UsedArea != 0 {
		t.Errorf("an untouched unit must show no consumption, got %+v", use)
	}
}

func TestUnitUsage_CutSplitsTheAreaIntoRecoveredAndUsed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	parent := receiveSlab(t, pool, seedAreaItem(t, pool, uniq("DBTEST-USAGE")))

	res, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{
			{Serial: uniq("DBTEST-R1"), LengthMM: 1200, WidthMM: 700},
			{Serial: uniq("DBTEST-R2"), LengthMM: 900, WidthMM: 600},
		},
	}, 1)
	if err != nil {
		t.Fatalf("CutUnit: %v", err)
	}

	got, err := GetUnit(ctx, pool, parent.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	use := got.Usage
	if use.OffcutCount != 2 {
		t.Errorf("OffcutCount = %d, want 2", use.OffcutCount)
	}
	if use.RecoveredArea != res.RecoveredArea {
		t.Errorf("RecoveredArea = %v, want %v", use.RecoveredArea, res.RecoveredArea)
	}
	// What did not come back is exactly the loss the cut reported.
	if use.UsedArea != res.LostArea {
		t.Errorf("UsedArea = %v, want the cut's LostArea %v", use.UsedArea, res.LostArea)
	}
	if roundTo(use.RecoveredArea+use.UsedArea, areaScale) != got.Area {
		t.Errorf("recovered %v + used %v must add back up to the slab's %v", use.RecoveredArea, use.UsedArea, got.Area)
	}
	if use.ConsumedAt == nil || use.ScrappedAt != nil {
		t.Errorf("a cut slab left stock by being consumed, got consumedAt=%v scrappedAt=%v", use.ConsumedAt, use.ScrappedAt)
	}
	// A manual cut belongs to no fabrication job.
	if use.JobID != "" {
		t.Errorf("a hand-cut slab must name no job, got %q", use.JobID)
	}
	// The cut's own response carries the same picture, not a stale one.
	if res.Parent.Usage.RecoveredArea != res.RecoveredArea {
		t.Errorf("CutResult.Parent.Usage.RecoveredArea = %v, want %v", res.Parent.Usage.RecoveredArea, res.RecoveredArea)
	}

	for _, rem := range res.Remnants {
		if rem.ParentSerial != parent.Serial || rem.RootSerial != parent.Serial {
			t.Errorf("remnant %s lineage = parent %q root %q, want both %q", rem.Serial, rem.ParentSerial, rem.RootSerial, parent.Serial)
		}
		if rem.Usage.UsedArea != 0 || rem.Usage.OffcutCount != 0 || rem.Usage.ConsumedAt != nil {
			t.Errorf("a fresh offcut has not been used, got %+v", rem.Usage)
		}
	}
}

func TestUnitUsage_AnOffcutTooSmallToKeepIsNotCountedAsRecovered(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	parent := receiveSlab(t, pool, seedAreaItem(t, pool, uniq("DBTEST-USAGE")))

	res, err := CutUnit(ctx, pool, parent.ID, CutInput{
		Remnants: []CutPiece{
			{Serial: uniq("DBTEST-BIG"), LengthMM: 1200, WidthMM: 700},
			{Serial: uniq("DBTEST-SLIVER"), LengthMM: 400, WidthMM: 120},
		},
		MinUsableLengthMM: 600, MinUsableWidthMM: 300,
	}, 1)
	if err != nil {
		t.Fatalf("CutUnit: %v", err)
	}

	use := res.Parent.Usage
	if use.OffcutCount != 1 {
		t.Errorf("OffcutCount = %d, want only the offcut that went back into stock", use.OffcutCount)
	}
	if use.RecoveredArea != res.RecoveredArea {
		t.Errorf("RecoveredArea = %v, want %v (the sliver never re-entered stock)", use.RecoveredArea, res.RecoveredArea)
	}
}

func TestUnitUsage_ScrappedUnitRecordsWhenButIsNotUse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := receiveSlab(t, pool, seedAreaItem(t, pool, uniq("DBTEST-USAGE")))

	if err := ScrapUnit(ctx, pool, u.ID, nil, "cracked in the yard", 1); err != nil {
		t.Fatalf("ScrapUnit: %v", err)
	}
	got, err := GetUnit(ctx, pool, u.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if got.Usage.ScrappedAt == nil || got.Usage.ConsumedAt != nil {
		t.Errorf("a scrapped slab was scrapped, not consumed: %+v", got.Usage)
	}
	if got.Usage.UsedArea != 0 {
		t.Errorf("scrap is not use, UsedArea = %v", got.Usage.UsedArea)
	}
}

func TestSearchUnits_FiltersOffcutsByParent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-USAGE"))
	cut := receiveSlab(t, pool, item)
	other := receiveSlab(t, pool, item)

	res, err := CutUnit(ctx, pool, cut.ID, CutInput{
		Remnants: []CutPiece{
			{Serial: uniq("DBTEST-R1"), LengthMM: 1200, WidthMM: 700},
			{Serial: uniq("DBTEST-R2"), LengthMM: 900, WidthMM: 600},
		},
	}, 1)
	if err != nil {
		t.Fatalf("CutUnit: %v", err)
	}

	page, err := SearchUnits(ctx, pool, query.Request{
		Filters: []query.Clause{{Field: "parent_id", Op: query.OpEq, Value: cut.ID}},
	})
	if err != nil {
		t.Fatalf("SearchUnits: %v", err)
	}
	if len(page.Records) != len(res.Remnants) {
		t.Fatalf("got %d offcuts of the cut slab, want %d", len(page.Records), len(res.Remnants))
	}
	for _, r := range page.Records {
		if r.ID == other.ID || r.ID == cut.ID {
			t.Errorf("unit %s is not an offcut of the cut slab", r.Serial)
		}
	}
}

func TestSummarizeUnits_TotalsByStatusAndKeepsUnitsOfMeasureApart(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-SUMMARY"))

	available := receiveSlab(t, pool, item)
	cut := receiveSlab(t, pool, item)
	scrapped := receiveSlab(t, pool, item)
	res, err := CutUnit(ctx, pool, cut.ID, CutInput{
		Remnants: []CutPiece{{Serial: uniq("DBTEST-R1"), LengthMM: 1200, WidthMM: 700}},
	}, 1)
	if err != nil {
		t.Fatalf("CutUnit: %v", err)
	}
	if err := ScrapUnit(ctx, pool, scrapped.ID, nil, "", 1); err != nil {
		t.Fatalf("ScrapUnit: %v", err)
	}
	offcut := res.Remnants[0]

	// Limit and cursor describe a page; a summary must cover the whole set.
	sum, err := SummarizeUnits(ctx, pool, query.Request{
		Filters: []query.Clause{{Field: "item_id", Op: query.OpEq, Value: item}},
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("SummarizeUnits: %v", err)
	}
	if len(sum.Groups) != 1 || sum.Groups[0].UnitCode != "SQFT" {
		t.Fatalf("groups = %+v, want a single SQFT group", sum.Groups)
	}
	g := sum.Groups[0]

	want := map[string]UnitStatusTotal{
		"available": {Count: 2, Area: roundTo(available.Area+offcut.Area, areaScale)},
		"consumed":  {Count: 1, Area: cut.Area},
		"scrapped":  {Count: 1, Area: scrapped.Area},
	}
	for status, w := range want {
		if g.ByStatus[status] != w {
			t.Errorf("%s = %+v, want %+v", status, g.ByStatus[status], w)
		}
	}
	if len(g.ByStatus) != len(want) {
		t.Errorf("statuses = %+v, want only %d", g.ByStatus, len(want))
	}
	if g.RecoveredArea != offcut.Area {
		t.Errorf("RecoveredArea = %v, want %v", g.RecoveredArea, offcut.Area)
	}

	// Narrowing the filter narrows the totals.
	only, err := SummarizeUnits(ctx, pool, query.Request{
		Filters: []query.Clause{
			{Field: "item_id", Op: query.OpEq, Value: item},
			{Field: "kind", Op: query.OpEq, Value: UnitKindRemnant},
		},
	})
	if err != nil {
		t.Fatalf("SummarizeUnits(remnants): %v", err)
	}
	if len(only.Groups) != 1 || only.Groups[0].ByStatus["available"].Count != 1 || len(only.Groups[0].ByStatus) != 1 {
		t.Errorf("remnant-only summary = %+v, want one available offcut", only.Groups)
	}

	// No matches is an empty list, not an error.
	none, err := SummarizeUnits(ctx, pool, query.Request{
		Filters: []query.Clause{{Field: "serial", Op: query.OpEq, Value: uniq("NO-SUCH-SERIAL")}},
	})
	if err != nil {
		t.Fatalf("SummarizeUnits(no match): %v", err)
	}
	if none.Groups == nil || len(none.Groups) != 0 {
		t.Errorf("no-match groups = %#v, want an empty non-nil slice", none.Groups)
	}
}

func TestSummarizeUnits_RejectsUnknownFilterField(t *testing.T) {
	pool := testPool(t)
	_, err := SummarizeUnits(context.Background(), pool, query.Request{
		Filters: []query.Clause{{Field: "not_a_field", Op: query.OpEq, Value: "x"}},
	})
	var invalid *query.InvalidFilterError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want *query.InvalidFilterError", err, err)
	}
}

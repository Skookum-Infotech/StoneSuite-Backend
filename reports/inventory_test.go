package reports

import "testing"

func TestAvailableQty(t *testing.T) {
	cases := []struct{ onHand, allocated, want float64 }{
		{100, 30, 70}, {100, 100, 0}, {10, 25, 0}, {0, 0, 0},
	}
	for _, c := range cases {
		if got := availableQty(c.onHand, c.allocated); !approx(got, c.want) {
			t.Errorf("availableQty(%v,%v) = %v want %v", c.onHand, c.allocated, got, c.want)
		}
	}
}

func TestStockRowAndTotals(t *testing.T) {
	a := finishStockRow(StockRow{ItemUUID: "i1", Location: "Main", OnHand: 10, Allocated: 4, UnitCost: 25})
	b := finishStockRow(StockRow{ItemUUID: "i1", Location: "Yard", OnHand: 5, UnitCost: 25})
	c := finishStockRow(StockRow{ItemUUID: "i2", Location: "Main", OnHand: 2, UnitCost: 100})
	if !approx(a.Value, 250) || !approx(a.Available, 6) {
		t.Errorf("row = %+v", a)
	}
	tot := sumStock([]StockRow{a, b, c})
	if !approx(tot.Value, 575) || tot.Items != 2 || tot.Locations != 2 || tot.Rows != 3 {
		t.Errorf("totals = %+v", tot)
	}
	if got := sumStock(nil); got.Items != 0 || got.Value != 0 {
		t.Errorf("empty totals = %+v", got)
	}
}

func TestSlabStatusListsAlign(t *testing.T) {
	if len(SlabStatuses) != len(SlabStatusLabels) {
		t.Fatalf("%d statuses but %d labels", len(SlabStatuses), len(SlabStatusLabels))
	}
	seen := map[string]bool{}
	for _, s := range SlabStatuses {
		if seen[s] || s == "consumed" || s == "scrapped" {
			t.Errorf("status %q must be unique and live", s)
		}
		seen[s] = true
	}
}

func TestSlabRowAndTotals(t *testing.T) {
	r1 := newSlabRow()
	r1.Counts[0], r1.Areas[0] = 3, 30.5
	r1.Counts[1], r1.Areas[1] = 1, 9.5
	r1 = finishSlabRow(r1)
	r2 := newSlabRow()
	r2.Counts[0], r2.Areas[0] = 2, 20
	r2 = finishSlabRow(r2)
	if r1.TotalSlabs != 4 || !approx(r1.TotalArea, 40) {
		t.Errorf("r1 = %+v", r1)
	}
	tot := sumSlabs([]SlabRow{r1, r2})
	if tot.Counts[0] != 5 || !approx(tot.Areas[0], 50.5) || tot.TotalSlabs != 6 {
		t.Errorf("totals = %+v", tot)
	}
	if len(sumSlabs(nil).Counts) != len(SlabStatuses) {
		t.Error("empty totals must still carry every status")
	}
}

func TestSuggestedOrderAndState(t *testing.T) {
	cases := []struct {
		reorder, onHand, onOrder, want float64
	}{
		{100, 20, 0, 80}, {100, 20, 50, 30}, {100, 20, 80, 0}, {100, 20, 200, 0}, {10, 0, 0, 10},
	}
	for _, c := range cases {
		if got := suggestedOrder(c.reorder, c.onHand, c.onOrder); !approx(got, c.want) {
			t.Errorf("suggestedOrder(%v,%v,%v) = %v want %v", c.reorder, c.onHand, c.onOrder, got, c.want)
		}
	}
	if lowStockState(0) != StateOutOfStock || lowStockState(-1) != StateOutOfStock || lowStockState(0.5) != StateBelow {
		t.Error("state classification wrong")
	}
}

func TestLowStockTotals(t *testing.T) {
	rows := []LowStockRow{
		finishLowStockRow(LowStockRow{ItemUUID: "a", OnHand: 0, ReorderPoint: 5}),
		finishLowStockRow(LowStockRow{ItemUUID: "a", OnHand: 2, ReorderPoint: 5}),
		finishLowStockRow(LowStockRow{ItemUUID: "b", OnHand: 3, ReorderPoint: 5}),
	}
	got := sumLowStock(rows)
	if got.Rows != 3 || got.OutOfStock != 1 || got.Items != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestSumMovements(t *testing.T) {
	got := sumMovements([]MovementRow{
		{Net: 10, Movements: 2}, {Net: -4, Movements: 3}, {Net: 0, Movements: 1},
	})
	if got.Items != 3 || got.Movements != 6 || got.Growing != 1 || got.Shrinking != 1 {
		t.Errorf("got %+v", got)
	}
}

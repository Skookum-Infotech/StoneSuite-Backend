package reports

import (
	"testing"
	"time"
)

func TestAgingBucketIndex(t *testing.T) {
	cases := []struct {
		days, want int
	}{
		{-10, 0}, {0, 0}, {1, 1}, {30, 1}, {31, 2}, {60, 2}, {61, 3}, {90, 3}, {91, 4}, {400, 4},
	}
	for _, c := range cases {
		if got := AgingBucketIndex(c.days); got != c.want {
			t.Errorf("AgingBucketIndex(%d) = %d want %d", c.days, got, c.want)
		}
	}
	if len(AgingBucketLabels) != 5 {
		t.Errorf("expected 5 aging labels, got %d", len(AgingBucketLabels))
	}
}

func TestCostChangePercent(t *testing.T) {
	if got := costChangePercent(0, 10); got != nil {
		t.Errorf("no starting cost should be nil, got %v", *got)
	}
	cases := []struct {
		first, last, want float64
	}{
		{100, 110, 10}, {100, 80, -20}, {50, 50, 0},
	}
	for _, c := range cases {
		got := costChangePercent(c.first, c.last)
		if got == nil || !approx(*got, c.want) {
			t.Errorf("costChangePercent(%v,%v) = %v want %v", c.first, c.last, got, c.want)
		}
	}
}

func TestFinishRowsAndTotals(t *testing.T) {
	v := finishVendorRow(VendorRow{POCount: 4, POValue: 1000})
	if !approx(v.AvgPOValue, 250) {
		t.Errorf("avg PO value = %v", v.AvgPOValue)
	}
	if finishVendorRow(VendorRow{}).AvgPOValue != 0 {
		t.Error("no POs must not divide by zero")
	}
	tot := sumVendors([]VendorRow{{POCount: 1, POValue: 10, Billed: 5}, {POCount: 2, POValue: 20, BalanceOwed: 7}})
	if tot.POCount != 3 || !approx(tot.POValue, 30) || !approx(tot.Billed, 5) || !approx(tot.BalanceOwed, 7) {
		t.Errorf("vendor totals = %+v", tot)
	}
	it := finishItemRow(PurchaseItemRow{Quantity: 10, Spend: 250, FirstCost: 20, LastCost: 30})
	if !approx(it.AvgCost, 25) || it.CostChange == nil || !approx(*it.CostChange, 50) {
		t.Errorf("item row = %+v", it)
	}
	if finishItemRow(PurchaseItemRow{}).AvgCost != 0 {
		t.Error("zero quantity must not divide by zero")
	}
}

func TestSumOpenPOs(t *testing.T) {
	late := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := sumOpenPOs([]OpenPORow{
		{Outstanding: 100, DaysOverdue: 5, ExpectedDate: &late},
		{Outstanding: 50},
		{Outstanding: 25, DaysOverdue: 1},
	})
	if got.Count != 3 || !approx(got.Outstanding, 175) || got.OverdueCount != 2 || !approx(got.OverdueValue, 125) {
		t.Errorf("got %+v", got)
	}
}

func TestSumAging(t *testing.T) {
	rows := []AgingRow{
		{Bills: 2, Total: 30, Buckets: []float64{10, 20, 0, 0, 0}},
		{Bills: 1, Total: 5, Buckets: []float64{0, 0, 0, 0, 5}},
	}
	got := sumAging(rows)
	want := []float64{10, 20, 0, 0, 5}
	for i, w := range want {
		if !approx(got.Buckets[i], w) {
			t.Errorf("bucket %d = %v want %v", i, got.Buckets[i], w)
		}
	}
	if got.Bills != 3 || !approx(got.Total, 35) {
		t.Errorf("totals = %+v", got)
	}
	if len(sumAging(nil).Buckets) != len(AgingBucketLabels) {
		t.Error("empty aging must still carry every bucket")
	}
}

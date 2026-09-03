package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
)

func TestMaterialConsumption_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/material-consumption/data", nil)
	rr := httptest.NewRecorder()
	h.MaterialConsumption(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestMaterialConsumption_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/material-consumption/data", nil)
	rr := httptest.NewRecorder()
	h.MaterialConsumption(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestNetUsedArea(t *testing.T) {
	tests := []struct {
		name                string
		consumed, recovered float64
		want                float64
	}{
		{"typical cut leaves a net used amount", 50, 20, 30},
		{"fully recovered nets to zero, not negative", 50, 50, 0},
		{"recovered exceeding consumed clamps to zero", 20, 50, 0},
		{"nothing recovered is the full consumed amount", 40, 0, 40},
		{"no activity at all is zero", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := netUsedArea(tt.consumed, tt.recovered); got != tt.want {
				t.Errorf("netUsedArea(%v, %v) = %v, want %v", tt.consumed, tt.recovered, got, tt.want)
			}
		})
	}
}

func TestBuildMaterialConsumption_Granted(t *testing.T) {
	rows := []inventory.ConsumptionRow{
		{ItemID: "big", ItemName: "Big User", UnitCode: "SQFT", ConsumedArea: 100, RecoveredArea: 10, SlabCount: 5},
		{ItemID: "small", ItemName: "Small User", UnitCode: "SQFT", ConsumedArea: 30, RecoveredArea: 5, SlabCount: 2},
		{ItemID: "fully-recovered", ItemName: "Fully Recovered", UnitCode: "SQFT", ConsumedArea: 20, RecoveredArea: 20, SlabCount: 1},
		{ItemID: "scrap-only", ItemName: "Scrap Only", UnitCode: "SQFT", ConsumedArea: 0, RecoveredArea: 0, ScrappedArea: 8, SlabCount: 0},
	}
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true}, nil }
	fetch := func() ([]inventory.ConsumptionRow, error) { return rows, nil }

	out, materialCount, slabTotal, ok, err := buildMaterialConsumption(check, fetch, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// fully-recovered nets to zero and scrap-only never consumed anything --
	// both dropped, leaving 2 real rows.
	if materialCount != 2 {
		t.Fatalf("materialCount = %d, want 2 (zero-net rows dropped)", materialCount)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].ID != "big" || out[1].ID != "small" {
		t.Fatalf("order = [%s, %s], want [big, small] (highest net used first)", out[0].ID, out[1].ID)
	}
	if out[0].NetUsed != 90 {
		t.Errorf("out[0].NetUsed = %v, want 90", out[0].NetUsed)
	}
	// slabTotal sums SlabCount across every counted row (5 + 2), not the
	// dropped ones.
	if slabTotal != 7 {
		t.Errorf("slabTotal = %d, want 7", slabTotal)
	}
}

func TestBuildMaterialConsumption_LimitTruncatesButCountsStayFull(t *testing.T) {
	rows := []inventory.ConsumptionRow{
		{ItemID: "a", ConsumedArea: 30, SlabCount: 1},
		{ItemID: "b", ConsumedArea: 20, SlabCount: 1},
		{ItemID: "c", ConsumedArea: 10, SlabCount: 1},
	}
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true}, nil }
	fetch := func() ([]inventory.ConsumptionRow, error) { return rows, nil }

	out, materialCount, slabTotal, ok, err := buildMaterialConsumption(check, fetch, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (truncated to limit)", len(out))
	}
	if materialCount != 3 {
		t.Fatalf("materialCount = %d, want 3 (full count, not truncated)", materialCount)
	}
	if slabTotal != 3 {
		t.Fatalf("slabTotal = %d, want 3 (summed over all rows, not truncated)", slabTotal)
	}
}

func TestBuildMaterialConsumption_NotGranted(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: false}, nil }
	fetch := func() ([]inventory.ConsumptionRow, error) {
		t.Fatal("fetch should not be called when the caller holds no grant")
		return nil, nil
	}

	_, _, _, ok, err := buildMaterialConsumption(check, fetch, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildMaterialConsumption_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{}, boom }
	fetch := func() ([]inventory.ConsumptionRow, error) {
		t.Fatal("fetch should not be called when check errors")
		return nil, nil
	}

	_, _, _, _, err := buildMaterialConsumption(check, fetch, 5)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildMaterialConsumption_PropagatesFetchError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true}, nil }
	fetch := func() ([]inventory.ConsumptionRow, error) { return nil, boom }

	_, _, _, _, err := buildMaterialConsumption(check, fetch, 5)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

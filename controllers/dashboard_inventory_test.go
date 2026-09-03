package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
)

func TestInventoryAlerts_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/inventory-alerts/data", nil)
	rr := httptest.NewRecorder()
	h.InventoryAlerts(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestInventoryAlerts_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/inventory-alerts/data", nil)
	rr := httptest.NewRecorder()
	h.InventoryAlerts(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestClassifyStockAlert(t *testing.T) {
	tests := []struct {
		name                  string
		onHand, allocated, rp float64
		wantSeverity          string
		wantGap               float64
		wantOK                bool
	}{
		{"allocated exceeds on-hand is short", 4, 10, 0, "short", 6, true},
		{"allocated exceeds on-hand even with a reorder point set", 4, 10, 2, "short", 6, true},
		{"zero on-hand with nothing allocated is out", 0, 0, 0, "out", 0, true},
		{"negative-impossible guard: on-hand exactly zero with a reorder point is out, not low", 0, 0, 5, "out", 5, true},
		{"on-hand at or below a configured reorder point is low", 6, 0, 12, "low", 6, true},
		{"on-hand exactly at the reorder point is low (boundary is inclusive)", 10, 0, 10, "low", 0, true},
		{"on-hand one above the reorder point is healthy", 11, 0, 10, "", 0, false},
		{"allocated exactly equal to on-hand is not short (boundary is exclusive)", 10, 10, 0, "", 0, false},
		{"zero reorder point never triggers low on its own", 5, 0, 0, "", 0, false},
		{"healthy stock with no allocation and no reorder point is not an alert", 20, 0, 0, "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			severity, gap, ok := classifyStockAlert(tt.onHand, tt.allocated, tt.rp)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if severity != tt.wantSeverity {
				t.Errorf("severity = %q, want %q", severity, tt.wantSeverity)
			}
			if gap != tt.wantGap {
				t.Errorf("gap = %v, want %v", gap, tt.wantGap)
			}
		})
	}
}

func TestBuildInventoryAlerts_Granted(t *testing.T) {
	levels := []inventory.StockLevel{
		{ItemID: "healthy", ItemName: "Healthy Item", WarehouseName: "W1", OnHand: 50, Allocated: 0, ReorderPoint: 10},
		{ItemID: "low-small-gap", ItemName: "Low Small Gap", WarehouseName: "W1", OnHand: 9, Allocated: 0, ReorderPoint: 10},
		{ItemID: "short", ItemName: "Short Item", WarehouseName: "W1", OnHand: 4, Allocated: 10, ReorderPoint: 0},
		{ItemID: "out", ItemName: "Out Item", WarehouseName: "W2", OnHand: 0, Allocated: 0, ReorderPoint: 0},
		{ItemID: "low-big-gap", ItemName: "Low Big Gap", WarehouseName: "W2", OnHand: 1, Allocated: 0, ReorderPoint: 20},
	}
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true}, nil }
	fetch := func() ([]inventory.StockLevel, error) { return levels, nil }

	alerts, alertCount, ok, err := buildInventoryAlerts(check, fetch, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if alertCount != 4 {
		t.Fatalf("alertCount = %d, want 4 (healthy row excluded)", alertCount)
	}
	if len(alerts) != 4 {
		t.Fatalf("len(alerts) = %d, want 4", len(alerts))
	}
	// short (tier 0) first, then out (tier 1), then low ordered by biggest
	// gap first: low-big-gap (19) before low-small-gap (1).
	gotIDs := []string{alerts[0].ID, alerts[1].ID, alerts[2].ID, alerts[3].ID}
	wantIDs := []string{"short", "out", "low-big-gap", "low-small-gap"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("alerts[%d].ID = %q, want %q (order was %v)", i, gotIDs[i], wantIDs[i], gotIDs)
		}
	}
	if alerts[0].Severity != "short" {
		t.Errorf("alerts[0].Severity = %q, want short", alerts[0].Severity)
	}
}

func TestBuildInventoryAlerts_LimitTruncatesButAlertCountStaysFull(t *testing.T) {
	levels := []inventory.StockLevel{
		{ItemID: "a", OnHand: 0},
		{ItemID: "b", OnHand: 0},
		{ItemID: "c", OnHand: 0},
	}
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true}, nil }
	fetch := func() ([]inventory.StockLevel, error) { return levels, nil }

	alerts, alertCount, ok, err := buildInventoryAlerts(check, fetch, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(alerts) != 2 {
		t.Fatalf("len(alerts) = %d, want 2 (truncated to limit)", len(alerts))
	}
	if alertCount != 3 {
		t.Fatalf("alertCount = %d, want 3 (full candidate count, not truncated)", alertCount)
	}
}

func TestBuildInventoryAlerts_NotGranted(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: false}, nil }
	fetch := func() ([]inventory.StockLevel, error) {
		t.Fatal("fetch should not be called when the caller holds no grant")
		return nil, nil
	}

	_, _, ok, err := buildInventoryAlerts(check, fetch, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildInventoryAlerts_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{}, boom }
	fetch := func() ([]inventory.StockLevel, error) {
		t.Fatal("fetch should not be called when check errors")
		return nil, nil
	}

	_, _, _, err := buildInventoryAlerts(check, fetch, 5)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildInventoryAlerts_PropagatesFetchError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true}, nil }
	fetch := func() ([]inventory.StockLevel, error) { return nil, boom }

	_, _, _, err := buildInventoryAlerts(check, fetch, 5)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

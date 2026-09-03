package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
)

func TestSalesOrdersSnapshot_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/sales-orders-snapshot/data", nil)
	rr := httptest.NewRecorder()
	h.SalesOrdersSnapshot(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestSalesOrdersSnapshot_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/sales-orders-snapshot/data", nil)
	rr := httptest.NewRecorder()
	h.SalesOrdersSnapshot(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestSummarizeOpenBacklog(t *testing.T) {
	openCodes := []string{"APPV", "OPEN", "PART"}

	t.Run("empty input reads all zero", func(t *testing.T) {
		out := summarizeOpenBacklog(nil, openCodes)
		if out.OpenCount != 0 || out.OpenValue != 0 || out.LateCount != 0 || out.LateValue != 0 {
			t.Fatalf("got %+v, want all zero", out)
		}
		if len(out.Statuses) != 0 {
			t.Fatalf("Statuses = %+v, want empty", out.Statuses)
		}
	})

	t.Run("excludes draft and pending from open totals but keeps them in the breakdown", func(t *testing.T) {
		buckets := []salesorder.StatusBucket{
			{Code: "DRFT", Label: "Draft", Count: 3, Value: 18400},
			{Code: "PAPV", Label: "Pending approval", Count: 2, Value: 9200},
			{Code: "APPV", Label: "Approved", Count: 5, Value: 61000},
			{Code: "OPEN", Label: "Open", Count: 11, Value: 240300},
			{Code: "PART", Label: "Partial", Count: 4, Value: 111000},
		}
		out := summarizeOpenBacklog(buckets, openCodes)

		if out.OpenCount != 20 { // 5 + 11 + 4
			t.Errorf("OpenCount = %d, want 20", out.OpenCount)
		}
		if out.OpenValue != 412300 { // 61000 + 240300 + 111000
			t.Errorf("OpenValue = %v, want 412300", out.OpenValue)
		}
		if len(out.Statuses) != 5 {
			t.Fatalf("Statuses = %+v, want all 5 buckets present (draft/pending not dropped)", out.Statuses)
		}
	})

	t.Run("sums late count/value only across open-set statuses", func(t *testing.T) {
		buckets := []salesorder.StatusBucket{
			{Code: "DRFT", Count: 3, Value: 18400, LateCount: 1, LateValue: 4000}, // must not count -- draft has no real commitment
			{Code: "APPV", Count: 5, Value: 61000, LateCount: 1, LateValue: 12000},
			{Code: "OPEN", Count: 11, Value: 240300, LateCount: 2, LateValue: 35250},
		}
		out := summarizeOpenBacklog(buckets, openCodes)

		if out.LateCount != 3 { // 1 (APPV) + 2 (OPEN), NOT the draft's 1
			t.Errorf("LateCount = %d, want 3", out.LateCount)
		}
		if out.LateValue != 47250 { // 12000 + 35250
			t.Errorf("LateValue = %v, want 47250", out.LateValue)
		}
	})
}

func TestMapAtRisk(t *testing.T) {
	late := 12
	dueSoon := -3
	rows := []salesorder.AtRiskOrder{
		{ID: "so-1", RecordNumber: "SO-1042", Customer: "Fontaine Builders", Value: 28400, Status: "Open", DaysLate: &late},
		{ID: "so-2", RecordNumber: "SO-1027", Customer: "Whitmore Residence", Value: 21750, Status: "Approved", DaysLate: &dueSoon},
		{ID: "so-3", RecordNumber: "SO-1019", Customer: "Bellwood Design Group", Value: 33500, Status: "Open", DaysLate: nil},
	}

	out := mapAtRisk(rows)

	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].ID != "so-1" || out[0].RecordNumber != "SO-1042" || out[0].Customer != "Fontaine Builders" ||
		out[0].Value != 28400 || out[0].Status != "Open" || out[0].DaysLate == nil || *out[0].DaysLate != 12 {
		t.Errorf("out[0] = %+v, want mapped from rows[0]", out[0])
	}
	if out[2].DaysLate != nil {
		t.Errorf("out[2].DaysLate = %v, want nil (no expected delivery date)", out[2].DaysLate)
	}
}

func TestBuildSalesOrdersSnapshot_Granted(t *testing.T) {
	snap := salesorder.SnapshotResult{
		Statuses: []salesorder.StatusBucket{
			{Code: "OPEN", Label: "Open", Count: 11, Value: 240300, LateCount: 2, LateValue: 35250},
		},
		AtRisk: []salesorder.AtRiskOrder{
			{ID: "so-1", RecordNumber: "SO-1042", Customer: "Fontaine Builders", Value: 28400, Status: "Open"},
		},
	}
	check := func() (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	var gotScope authz.Scope
	fetch := func(scope authz.Scope) (salesorder.SnapshotResult, error) {
		gotScope = scope
		return snap, nil
	}

	result, ok, err := buildSalesOrdersSnapshot(check, fetch, salesorder.OpenStatusCodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if gotScope != authz.ScopeAll {
		t.Errorf("fetch called with scope %q, want %q", gotScope, authz.ScopeAll)
	}
	if result.OpenCount != 11 {
		t.Errorf("OpenCount = %d, want 11", result.OpenCount)
	}
	if len(result.AtRisk) != 1 || result.AtRisk[0].RecordNumber != "SO-1042" {
		t.Errorf("AtRisk = %+v, want 1 row mapped from snap.AtRisk", result.AtRisk)
	}
}

func TestBuildSalesOrdersSnapshot_NotGranted(t *testing.T) {
	check := func() (authz.Decision, error) {
		return authz.Decision{Allowed: false}, nil
	}
	fetch := func(scope authz.Scope) (salesorder.SnapshotResult, error) {
		t.Fatal("fetch should not be called when the caller holds no grant")
		return salesorder.SnapshotResult{}, nil
	}

	_, ok, err := buildSalesOrdersSnapshot(check, fetch, salesorder.OpenStatusCodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildSalesOrdersSnapshot_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{}, boom }
	fetch := func(scope authz.Scope) (salesorder.SnapshotResult, error) {
		t.Fatal("fetch should not be called when check errors")
		return salesorder.SnapshotResult{}, nil
	}

	_, _, err := buildSalesOrdersSnapshot(check, fetch, salesorder.OpenStatusCodes)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildSalesOrdersSnapshot_PropagatesFetchError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}, nil
	}
	fetch := func(scope authz.Scope) (salesorder.SnapshotResult, error) {
		return salesorder.SnapshotResult{}, boom
	}

	_, _, err := buildSalesOrdersSnapshot(check, fetch, salesorder.OpenStatusCodes)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

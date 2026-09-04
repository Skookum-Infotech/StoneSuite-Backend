package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"stonesuite-backend/accountingperiod"
	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
)

func TestAccountingSnapshot_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/accounting-snapshot/data", nil)
	rr := httptest.NewRecorder()
	h.AccountingSnapshot(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAccountingSnapshot_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/accounting-snapshot/data", nil)
	rr := httptest.NewRecorder()
	h.AccountingSnapshot(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestMapRecentEntries(t *testing.T) {
	when := time.Date(2026, 9, 4, 8, 12, 0, 0, time.UTC)
	rows := []cashtransfer.RecentEntry{
		{UUID: "je-1", Number: "JE-000231", Description: "Fabrication labor accrual", Amount: 4200, Date: when},
	}
	out := mapRecentEntries(rows)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].ID != "je-1" || out[0].Number != "JE-000231" || out[0].Amount != 4200 {
		t.Errorf("out[0] = %+v, want je-1/JE-000231/4200", out[0])
	}
	// The widget formats "2h ago" itself, so the timestamp must survive
	// mapping intact rather than being pre-rendered to a string.
	if !out[0].Date.Equal(when) {
		t.Errorf("out[0].Date = %v, want %v", out[0].Date, when)
	}
}

func periodFixture() *accountingperiod.Period {
	return &accountingperiod.Period{
		Name:   "Aug 2026",
		Status: "open",
		Start:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
	}
}

func TestBuildAccountingSnapshot_Granted(t *testing.T) {
	period := periodFixture()
	check := func() (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	fetchPeriod := func() (*accountingperiod.Period, error) { return period, nil }
	var gotFrom, gotTo time.Time
	countEntries := func(scope authz.Scope, from, to time.Time) (int, error) {
		gotFrom, gotTo = from, to
		return 47, nil
	}
	fetchRecent := func(scope authz.Scope) ([]cashtransfer.RecentEntry, error) {
		return []cashtransfer.RecentEntry{{UUID: "je-1", Number: "JE-000231", Amount: 4200}}, nil
	}

	result, ok, err := buildAccountingSnapshot(check, fetchPeriod, countEntries, fetchRecent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !gotFrom.Equal(period.Start) || !gotTo.Equal(period.End) {
		t.Errorf("counted over %v..%v, want the period's own %v..%v", gotFrom, gotTo, period.Start, period.End)
	}
	if result.Period == nil {
		t.Fatal("Period = nil, want the current period")
	}
	if result.Period.Name != "Aug 2026" || result.Period.Status != "open" || result.Period.EntryCount != 47 {
		t.Errorf("Period = %+v, want Aug 2026/open/47", *result.Period)
	}
	// EntryTotal is the period-wide count, not the length of the truncated
	// list -- it feeds the widget's "N more entries" hint.
	if result.EntryTotal != 47 {
		t.Errorf("EntryTotal = %d, want 47", result.EntryTotal)
	}
	if len(result.Entries) != 1 {
		t.Errorf("Entries = %+v, want 1 row", result.Entries)
	}
}

func TestBuildAccountingSnapshot_NoCalendarConfigured(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	// A tenant that never ran calendar Setup has no period covering today.
	fetchPeriod := func() (*accountingperiod.Period, error) { return nil, nil }
	countEntries := func(scope authz.Scope, from, to time.Time) (int, error) {
		t.Fatal("countEntries should not be called when there is no period to count over")
		return 0, nil
	}
	fetchRecent := func(scope authz.Scope) ([]cashtransfer.RecentEntry, error) {
		return []cashtransfer.RecentEntry{{UUID: "je-1"}, {UUID: "je-2"}}, nil
	}

	result, ok, err := buildAccountingSnapshot(check, fetchPeriod, countEntries, fetchRecent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if result.Period != nil {
		t.Errorf("Period = %+v, want nil so the widget can render its setup empty state", *result.Period)
	}
	// Entries still list, and EntryTotal falls back to what was listed.
	if len(result.Entries) != 2 || result.EntryTotal != 2 {
		t.Errorf("Entries/EntryTotal = %d/%d, want 2/2", len(result.Entries), result.EntryTotal)
	}
}

func TestBuildAccountingSnapshot_NotGranted(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: false}, nil }
	fetchPeriod := func() (*accountingperiod.Period, error) {
		t.Fatal("fetchPeriod should not be called when the caller holds no grant")
		return nil, nil
	}
	countEntries := func(scope authz.Scope, from, to time.Time) (int, error) { return 0, nil }
	fetchRecent := func(scope authz.Scope) ([]cashtransfer.RecentEntry, error) { return nil, nil }

	_, ok, err := buildAccountingSnapshot(check, fetchPeriod, countEntries, fetchRecent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildAccountingSnapshot_PropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	granted := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}, nil }
	somePeriod := func() (*accountingperiod.Period, error) { return periodFixture(), nil }
	someRecent := func(authz.Scope) ([]cashtransfer.RecentEntry, error) { return nil, nil }
	someCount := func(authz.Scope, time.Time, time.Time) (int, error) { return 0, nil }

	tests := []struct {
		name         string
		check        func() (authz.Decision, error)
		fetchPeriod  func() (*accountingperiod.Period, error)
		countEntries func(authz.Scope, time.Time, time.Time) (int, error)
		fetchRecent  func(authz.Scope) ([]cashtransfer.RecentEntry, error)
	}{
		{"check", func() (authz.Decision, error) { return authz.Decision{}, boom }, somePeriod, someCount, someRecent},
		{"fetchPeriod", granted, func() (*accountingperiod.Period, error) { return nil, boom }, someCount, someRecent},
		{"fetchRecent", granted, somePeriod, someCount, func(authz.Scope) ([]cashtransfer.RecentEntry, error) { return nil, boom }},
		{"countEntries", granted, somePeriod, func(authz.Scope, time.Time, time.Time) (int, error) { return 0, boom }, someRecent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := buildAccountingSnapshot(tc.check, tc.fetchPeriod, tc.countEntries, tc.fetchRecent)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want %v", err, boom)
			}
		})
	}
}

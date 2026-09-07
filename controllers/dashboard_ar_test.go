package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
)

func TestArOutstanding_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/ar-outstanding/data", nil)
	rr := httptest.NewRecorder()
	h.ArOutstanding(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestArOutstanding_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/ar-outstanding/data", nil)
	rr := httptest.NewRecorder()
	h.ArOutstanding(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestMapAgingBuckets_PreservesOrderAndZeroBuckets(t *testing.T) {
	// The widget renders one bar per bucket positionally, so an empty band
	// must survive mapping as a real zero rather than being dropped.
	buckets := []invoice.AgingBucket{
		{Label: "0-30", Amount: 13600, Count: 3},
		{Label: "31-60", Amount: 0, Count: 0},
		{Label: "61-90", Amount: 3100, Count: 1},
		{Label: "90+", Amount: 7600, Count: 2},
	}
	out := mapAgingBuckets(buckets)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	for i, want := range []string{"0-30", "31-60", "61-90", "90+"} {
		if out[i].Label != want {
			t.Errorf("out[%d].Label = %q, want %q", i, out[i].Label, want)
		}
	}
	if out[1].Amount != 0 || out[1].Count != 0 {
		t.Errorf("out[1] = %+v, want a zeroed 31-60 bucket", out[1])
	}
	if out[3].Amount != 7600 || out[3].Count != 2 {
		t.Errorf("out[3] = %+v, want 7600/2", out[3])
	}
}

func TestMapOutstandingInvoices(t *testing.T) {
	rows := []invoice.OutstandingInvoice{
		{UUID: "inv-1", Number: "INV-3155", Customer: "Meridian Countertops", BalanceDue: 7600, DaysPastDue: 98},
		{UUID: "inv-2", Number: "INV-3301", Customer: "Fontaine Builders", BalanceDue: 8400, DaysPastDue: 0},
	}
	out := mapOutstandingInvoices(rows)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].ID != "inv-1" || out[0].Number != "INV-3155" || out[0].DaysPastDue != 98 {
		t.Errorf("out[0] = %+v, want inv-1/INV-3155/98", out[0])
	}
	// An invoice with no due date reads as 0 days past due, not overdue.
	if out[1].DaysPastDue != 0 {
		t.Errorf("out[1].DaysPastDue = %d, want 0", out[1].DaysPastDue)
	}
}

func TestBuildArOutstanding_Granted(t *testing.T) {
	aging := invoice.AgingResult{
		Buckets: []invoice.AgingBucket{
			{Label: "0-30", Amount: 13600, Count: 3},
			{Label: "31-60", Amount: 12900, Count: 2},
			{Label: "61-90", Amount: 3100, Count: 1},
			{Label: "90+", Amount: 7600, Count: 2},
		},
		Outstanding:      37200,
		OutstandingCount: 8,
		OverdueTotal:     28800,
		OverdueCount:     4,
	}
	check := func() (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	var agingScope, oldestScope authz.Scope
	fetchAging := func(scope authz.Scope) (invoice.AgingResult, error) {
		agingScope = scope
		return aging, nil
	}
	fetchOldest := func(scope authz.Scope) ([]invoice.OutstandingInvoice, error) {
		oldestScope = scope
		return []invoice.OutstandingInvoice{{UUID: "inv-1", Number: "INV-3155", DaysPastDue: 98}}, nil
	}

	result, ok, err := buildArOutstanding(check, fetchAging, fetchOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if agingScope != authz.ScopeAll || oldestScope != authz.ScopeAll {
		t.Errorf("scopes = %q/%q, want both %q", agingScope, oldestScope, authz.ScopeAll)
	}
	if result.Outstanding != 37200 || result.OverdueTotal != 28800 || result.OverdueCount != 4 {
		t.Errorf("totals = %v/%v/%v, want 37200/28800/4", result.Outstanding, result.OverdueTotal, result.OverdueCount)
	}
	// OldestCount is every outstanding invoice, not the length of the
	// truncated worklist -- it feeds the widget's "N more" hint.
	if result.OldestCount != 8 {
		t.Errorf("OldestCount = %d, want 8", result.OldestCount)
	}
	if len(result.Oldest) != 1 || result.Oldest[0].ID != "inv-1" {
		t.Errorf("Oldest = %+v, want 1 row for inv-1", result.Oldest)
	}
}

func TestBuildArOutstanding_SkipsWorklistWhenNothingOutstanding(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchAging := func(scope authz.Scope) (invoice.AgingResult, error) {
		return invoice.AgingResult{Buckets: []invoice.AgingBucket{{Label: "0-30"}}}, nil
	}
	fetchOldest := func(scope authz.Scope) ([]invoice.OutstandingInvoice, error) {
		t.Fatal("fetchOldest should not be called when nothing is outstanding")
		return nil, nil
	}

	result, ok, err := buildArOutstanding(check, fetchAging, fetchOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// Serialized as [] rather than null so the widget can map over it.
	if result.Oldest == nil || len(result.Oldest) != 0 {
		t.Errorf("Oldest = %+v, want an empty non-nil slice", result.Oldest)
	}
}

func TestBuildArOutstanding_NotGranted(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: false}, nil }
	fetchAging := func(scope authz.Scope) (invoice.AgingResult, error) {
		t.Fatal("fetchAging should not be called when the caller holds no grant")
		return invoice.AgingResult{}, nil
	}
	fetchOldest := func(scope authz.Scope) ([]invoice.OutstandingInvoice, error) { return nil, nil }

	_, ok, err := buildArOutstanding(check, fetchAging, fetchOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildArOutstanding_PropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	granted := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}, nil }
	someAging := func(scope authz.Scope) (invoice.AgingResult, error) {
		return invoice.AgingResult{OutstandingCount: 1}, nil
	}

	tests := []struct {
		name        string
		check       func() (authz.Decision, error)
		fetchAging  func(authz.Scope) (invoice.AgingResult, error)
		fetchOldest func(authz.Scope) ([]invoice.OutstandingInvoice, error)
	}{
		{
			name:        "check",
			check:       func() (authz.Decision, error) { return authz.Decision{}, boom },
			fetchAging:  someAging,
			fetchOldest: func(authz.Scope) ([]invoice.OutstandingInvoice, error) { return nil, nil },
		},
		{
			name:        "fetchAging",
			check:       granted,
			fetchAging:  func(authz.Scope) (invoice.AgingResult, error) { return invoice.AgingResult{}, boom },
			fetchOldest: func(authz.Scope) ([]invoice.OutstandingInvoice, error) { return nil, nil },
		},
		{
			name:        "fetchOldest",
			check:       granted,
			fetchAging:  someAging,
			fetchOldest: func(authz.Scope) ([]invoice.OutstandingInvoice, error) { return nil, boom },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := buildArOutstanding(tc.check, tc.fetchAging, tc.fetchOldest)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want %v", err, boom)
			}
		})
	}
}

package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"stonesuite-backend/authz"
)

func TestPipelineMix_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/pipeline-donut/data", nil)
	rr := httptest.NewRecorder()
	h.PipelineMix(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestPipelineMix_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/pipeline-donut/data", nil)
	rr := httptest.NewRecorder()
	h.PipelineMix(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestParseDashboardRange(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		raw       string
		wantSince time.Time
		wantOK    bool
	}{
		{"empty defaults to unbounded", "", time.Time{}, true},
		{"all is unbounded", "all", time.Time{}, true},
		{"7d is 7 days back", "7d", now.AddDate(0, 0, -7), true},
		{"30d is 30 days back", "30d", now.AddDate(0, 0, -30), true},
		{"quarter is 90 days back", "quarter", now.AddDate(0, 0, -90), true},
		{"unknown value is rejected", "last-week", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			since, ok := parseDashboardRange(tt.raw, now)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !since.Equal(tt.wantSince) {
				t.Fatalf("since = %v, want %v", since, tt.wantSince)
			}
		})
	}
}

func TestPipelineCloseRate(t *testing.T) {
	tests := []struct {
		name            string
		counts          map[string]int
		customerGranted bool
		want            int
	}{
		{
			name:            "customer share of whole pipeline",
			counts:          map[string]int{"lead": 24, "prospect": 17, "customer": 9},
			customerGranted: true,
			want:            18, // 9 / 50 = 18%
		},
		{
			name:            "customer ungranted reads zero even if lead/prospect present",
			counts:          map[string]int{"lead": 24, "prospect": 17},
			customerGranted: false,
			want:            0,
		},
		{
			name:            "empty pipeline reads zero",
			counts:          map[string]int{},
			customerGranted: true,
			want:            0,
		},
		{
			name:            "all customers is 100%",
			counts:          map[string]int{"customer": 5},
			customerGranted: true,
			want:            100,
		},
		{
			name:            "rounds to nearest percent",
			counts:          map[string]int{"lead": 1, "prospect": 1, "customer": 1},
			customerGranted: true,
			want:            33, // 1/3 = 33.33...
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pipelineCloseRate(tt.counts, tt.customerGranted)
			if got != tt.want {
				t.Fatalf("pipelineCloseRate(%v, %v) = %d, want %d", tt.counts, tt.customerGranted, got, tt.want)
			}
		})
	}
}

func TestBuildPipelineMix_AllStagesGranted(t *testing.T) {
	counts := map[string]int{"lead": 24, "prospect": 17, "customer": 9}
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	count := func(key string, scope authz.Scope) (int, error) {
		return counts[key], nil
	}

	result, ok, err := buildPipelineMix(check, count)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(result.Segments) != 3 {
		t.Fatalf("Segments = %+v, want 3 entries", result.Segments)
	}
	want := map[string]int{"lead": 24, "prospect": 17, "customer": 9}
	for _, s := range result.Segments {
		if s.Count != want[s.ID] {
			t.Errorf("segment %q count = %d, want %d", s.ID, s.Count, want[s.ID])
		}
	}
	if result.CloseRate != 18 {
		t.Errorf("CloseRate = %d, want 18", result.CloseRate)
	}
}

func TestBuildPipelineMix_PartialGrantOmitsUngrantedStage(t *testing.T) {
	// Caller holds lead:read and prospect:read but not customer:read.
	check := func(resource authz.Resource) (authz.Decision, error) {
		if resource == authz.ResourceCustomer {
			return authz.Decision{Allowed: false}, nil
		}
		return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}, nil
	}
	count := func(key string, scope authz.Scope) (int, error) {
		return 5, nil
	}

	result, ok, err := buildPipelineMix(check, count)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(result.Segments) != 2 {
		t.Fatalf("Segments = %+v, want 2 entries (customer omitted)", result.Segments)
	}
	for _, s := range result.Segments {
		if s.ID == "customer" {
			t.Fatal("customer segment present despite no grant")
		}
	}
	// Close rate must read 0, not a share computed over only the granted
	// stages -- the caller can't see the customer stage at all, so there is
	// no meaningful "% closed" to show.
	if result.CloseRate != 0 {
		t.Errorf("CloseRate = %d, want 0 when customer stage is ungranted", result.CloseRate)
	}
}

func TestBuildPipelineMix_NoStageGranted(t *testing.T) {
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{Allowed: false}, nil
	}
	count := func(key string, scope authz.Scope) (int, error) {
		t.Fatalf("count should not be called for an ungranted stage (key=%s)", key)
		return 0, nil
	}

	result, ok, err := buildPipelineMix(check, count)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("ok = true, want false (no stage granted); result = %+v", result)
	}
}

func TestBuildPipelineMix_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{}, boom
	}
	count := func(key string, scope authz.Scope) (int, error) {
		return 0, nil
	}

	_, _, err := buildPipelineMix(check, count)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildPipelineMix_PropagatesCountError(t *testing.T) {
	boom := errors.New("boom")
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	count := func(key string, scope authz.Scope) (int, error) {
		return 0, boom
	}

	_, _, err := buildPipelineMix(check, count)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

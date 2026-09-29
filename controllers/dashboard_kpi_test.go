package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

func TestKpiStrip_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/kpi-strip/data", nil)
	rr := httptest.NewRecorder()
	h.KpiStrip(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestKpiStrip_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/kpi-strip/data", nil)
	rr := httptest.NewRecorder()
	h.KpiStrip(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestDashboardDeltaWindow(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		raw           string
		wantCurFrom   time.Time
		wantPriorFrom time.Time
		wantPriorTo   time.Time
	}{
		{"empty falls back to 7-day window", "", now.AddDate(0, 0, -7), now.AddDate(0, 0, -14), now.AddDate(0, 0, -7)},
		{"all falls back to 7-day window (no natural prior-all-time)", "all", now.AddDate(0, 0, -7), now.AddDate(0, 0, -14), now.AddDate(0, 0, -7)},
		{"7d window", "7d", now.AddDate(0, 0, -7), now.AddDate(0, 0, -14), now.AddDate(0, 0, -7)},
		{"30d window scales to 30 days each side", "30d", now.AddDate(0, 0, -30), now.AddDate(0, 0, -60), now.AddDate(0, 0, -30)},
		{"quarter window scales to 90 days each side", "quarter", now.AddDate(0, 0, -90), now.AddDate(0, 0, -180), now.AddDate(0, 0, -90)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			curFrom, priorFrom, priorTo := dashboardDeltaWindow(tt.raw, now)
			if !curFrom.Equal(tt.wantCurFrom) {
				t.Errorf("curFrom = %v, want %v", curFrom, tt.wantCurFrom)
			}
			if !priorFrom.Equal(tt.wantPriorFrom) {
				t.Errorf("priorFrom = %v, want %v", priorFrom, tt.wantPriorFrom)
			}
			if !priorTo.Equal(tt.wantPriorTo) {
				t.Errorf("priorTo = %v, want %v", priorTo, tt.wantPriorTo)
			}
			// The current window always ends exactly where the prior window
			// starts -- no gap, no overlap.
			if !curFrom.Equal(priorTo) {
				t.Errorf("curFrom (%v) must equal priorTo (%v) -- adjacent windows", curFrom, priorTo)
			}
		})
	}
}

func TestDeltaPct(t *testing.T) {
	tests := []struct {
		name           string
		current, prior float64
		want           *int
	}{
		{"18% increase", 118, 100, intPtr(18)},
		{"decrease is negative", 82, 100, intPtr(-18)},
		{"no change is zero", 100, 100, intPtr(0)},
		{"zero prior is undefined (nil)", 50, 0, nil},
		{"rounds to nearest percent", 133, 100, intPtr(33)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deltaPct(tt.current, tt.prior)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("deltaPct(%v, %v) = %v, want %v", tt.current, tt.prior, got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("deltaPct(%v, %v) = %d, want %d", tt.current, tt.prior, *got, *tt.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

// buildNeedsApproval's authorization model is approver-table membership
// (isConfiguredApprover, mirroring approvalchain/engine.go's Approve), NOT
// generic RBAC resource read/scope -- a manager who's a configured approver
// but holds no broad `<module>:read` grant (or holds it at scope=own, which
// would filter to records they themselves created, not records routed to
// them to approve) must still see their pending queue. See
// dashboard_kpi.go's doc comment for the full incident writeup.

func TestBuildNeedsApproval_SumsAcrossEligibleModules(t *testing.T) {
	keys := []string{"requisition", "purchase_order", "expense"}
	counts := map[string]int{"requisition": 3, "purchase_order": 2, "expense": 1}
	oldestByKey := map[string]*time.Time{
		"requisition":    timePtr(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)),
		"purchase_order": timePtr(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)), // oldest overall
		"expense":        timePtr(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)),
	}
	countAndOldest := func(key string) (int, *time.Time, bool, error) {
		return counts[key], oldestByKey[key], true, nil
	}

	total, oldest, anyEligible, err := buildNeedsApproval(keys, countAndOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 6 {
		t.Errorf("total = %d, want 6", total)
	}
	if !anyEligible {
		t.Error("anyEligible = false, want true")
	}
	if oldest == nil || !oldest.Equal(*oldestByKey["purchase_order"]) {
		t.Errorf("oldest = %v, want %v", oldest, oldestByKey["purchase_order"])
	}
}

// A manager who's a configured approver for requisition but not for
// purchase_order still sees their requisition queue -- ineligibility for
// one module never suppresses another.
func TestBuildNeedsApproval_ModuleIneligibilityDoesNotSuppressOthers(t *testing.T) {
	keys := []string{"requisition", "purchase_order"}
	countAndOldest := func(key string) (int, *time.Time, bool, error) {
		if key == "purchase_order" {
			return 0, nil, false, nil // not a configured approver for this module
		}
		return 4, timePtr(time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)), true, nil
	}

	total, oldest, anyEligible, err := buildNeedsApproval(keys, countAndOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
	if !anyEligible {
		t.Error("anyEligible = false, want true (eligible for requisition)")
	}
	if oldest == nil {
		t.Error("oldest = nil, want the requisition module's oldest timestamp")
	}
}

// A caller who is a configured approver for a module but currently has
// nothing pending must read as "0, caught up" (anyEligible=true), not be
// indistinguishable from someone who isn't an approver at all.
func TestBuildNeedsApproval_EligibleWithZeroPendingIsNotSameAsIneligible(t *testing.T) {
	keys := []string{"requisition"}
	countAndOldest := func(key string) (int, *time.Time, bool, error) {
		return 0, nil, true, nil
	}

	total, oldest, anyEligible, err := buildNeedsApproval(keys, countAndOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || oldest != nil {
		t.Errorf("total=%d oldest=%v, want 0/nil", total, oldest)
	}
	if !anyEligible {
		t.Error("anyEligible = false, want true -- caller is a configured approver, just caught up")
	}
}

func TestBuildNeedsApproval_NoEligibleModulesIsZeroNotError(t *testing.T) {
	keys := []string{"requisition"}
	countAndOldest := func(key string) (int, *time.Time, bool, error) {
		return 0, nil, false, nil
	}

	total, oldest, anyEligible, err := buildNeedsApproval(keys, countAndOldest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || oldest != nil || anyEligible {
		t.Errorf("total=%d oldest=%v anyEligible=%v, want 0/nil/false", total, oldest, anyEligible)
	}
}

func TestBuildNeedsApproval_PropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	keys := []string{"requisition"}
	countAndOldest := func(key string) (int, *time.Time, bool, error) { return 0, nil, false, boom }
	_, _, _, err := buildNeedsApproval(keys, countAndOldest)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// CRM (lead/prospect/customer) approval is a separate mechanism from the
// approvalchain registry entirely (crm_workflow_approver, not
// <module>_approver -- see crmstore/relational_approval.go's package doc,
// which explicitly says CRM is "intentionally not registered" in
// approvalchain). The first version of "Needs Approval" only iterated
// approvalchain.Keys() and so never surfaced a pending Lead/Prospect/
// Customer approval at all, regardless of who the caller was. Root-caused
// via /debug: "still not seeing the lead approval document for manager
// manuu."

// fakeApprovalStore is a minimal crmstore.Store test double: it embeds
// Store for every method this test doesn't exercise (a nil call to any of
// them would panic, which is fine -- these tests only call
// PendingApprovals), and overrides PendingApprovals to return canned
// results. Mirrors the fakeCountStore pattern in ai_analytical_test.go.
type fakeApprovalStore struct {
	crmstore.Store
	pending []workflow.Record
	err     error
}

func (f *fakeApprovalStore) PendingApprovals(_ context.Context, _ *pgxpool.Pool, _ string) ([]workflow.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pending, nil
}

func TestCrmPendingCountAndOldest_CountsAndFindsOldest(t *testing.T) {
	store := &fakeApprovalStore{pending: []workflow.Record{
		{ID: "1", CreatedAt: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)},
		{ID: "2", CreatedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)}, // oldest
		{ID: "3", CreatedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)},
	}}

	n, oldest, eligible, err := crmPendingCountAndOldest(context.Background(), store, nil, "identity-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("n = %d, want 3", n)
	}
	if !eligible {
		t.Error("eligible = false, want true")
	}
	want := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	if oldest == nil || !oldest.Equal(want) {
		t.Errorf("oldest = %v, want %v", oldest, want)
	}
}

func TestCrmPendingCountAndOldest_NoPendingIsIneligibleNotError(t *testing.T) {
	store := &fakeApprovalStore{pending: []workflow.Record{}}

	n, oldest, eligible, err := crmPendingCountAndOldest(context.Background(), store, nil, "identity-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 || oldest != nil || eligible {
		t.Errorf("n=%d oldest=%v eligible=%v, want 0/nil/false", n, oldest, eligible)
	}
}

func TestCrmPendingCountAndOldest_PropagatesError(t *testing.T) {
	boom := errors.New("boom")
	store := &fakeApprovalStore{err: boom}

	_, _, _, err := crmPendingCountAndOldest(context.Background(), store, nil, "identity-1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// The Open Leads KPI previously counted every lead ever created, unfiltered
// by status — despite its label — a bug found as a side effect of
// root-causing the AI assistant's own filtered-count logic
// (countCRMRecordsOpenClosed in controllers/ai_analytical_answer.go), which
// uses the same IsTerminal-based approach these tests cover: openStatusIDs
// narrows to non-terminal statuses, openRecordFilters turns that into a
// query.OpIn clause (ANDed with an optional created_at window), and
// countOpenRecordsFiltered wires the two together against the store.

func TestOpenStatusIDs_KeepsOnlyNonTerminal(t *testing.T) {
	tests := []struct {
		name     string
		statuses []workflow.StatusInfo
		want     []any
	}{
		{
			name: "mix of terminal and non-terminal",
			statuses: []workflow.StatusInfo{
				{StateID: "1", IsTerminal: false}, // New
				{StateID: "2", IsTerminal: false}, // In Progress
				{StateID: "3", IsTerminal: false}, // Qualified
				{StateID: "4", IsTerminal: true},  // Unqualified
				{StateID: "5", IsTerminal: true},  // Converted
				{StateID: "6", IsTerminal: true},  // Dead
			},
			want: []any{"1", "2", "3"},
		},
		{
			name:     "all terminal yields empty, not nil",
			statuses: []workflow.StatusInfo{{StateID: "1", IsTerminal: true}},
			want:     []any{},
		},
		{
			name:     "no statuses yields empty, not nil",
			statuses: nil,
			want:     []any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openStatusIDs(tt.statuses)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOpenRecordFilters(t *testing.T) {
	openIDs := []any{"1", "2"}
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		since, until time.Time
		want         []query.Clause
	}{
		{
			name:  "no bounds: status filter only",
			since: time.Time{}, until: time.Time{},
			want: []query.Clause{{Field: "status", Op: query.OpIn, Value: openIDs}},
		},
		{
			name:  "since only",
			since: since, until: time.Time{},
			want: []query.Clause{
				{Field: "status", Op: query.OpIn, Value: openIDs},
				{Field: "created_at", Op: query.OpGte, Value: since.UTC().Format(time.RFC3339)},
			},
		},
		{
			name:  "since and until",
			since: since, until: until,
			want: []query.Clause{
				{Field: "status", Op: query.OpIn, Value: openIDs},
				{Field: "created_at", Op: query.OpGte, Value: since.UTC().Format(time.RFC3339)},
				{Field: "created_at", Op: query.OpLt, Value: until.UTC().Format(time.RFC3339)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openRecordFilters(openIDs, tt.since, tt.until)
			assert.Equal(t, tt.want, got)
		})
	}
}

// fakeOpenLeadsStore is a minimal crmstore.Store test double for
// countOpenRecordsFiltered: embeds Store for every method these tests don't
// exercise (mirrors fakeApprovalStore above) and records each
// CountRecordsFiltered call for assertions.
type fakeOpenLeadsStore struct {
	crmstore.Store
	count int
	err   error
	calls []filteredCall
}

func (f *fakeOpenLeadsStore) CountRecordsFiltered(_ context.Context, _ *pgxpool.Pool, key, scope, actorIdentityID string, filters []query.Clause) (int, error) {
	f.calls = append(f.calls, filteredCall{key, scope, actorIdentityID, filters})
	if f.err != nil {
		return 0, f.err
	}
	return f.count, nil
}

func TestCountOpenRecordsFiltered_EmptyOpenIDsSkipsQueryEntirely(t *testing.T) {
	store := &fakeOpenLeadsStore{count: 99} // would be wrong if returned -- proves the store was never called
	n, err := countOpenRecordsFiltered(context.Background(), store, nil, "lead", "own", "identity-1", []any{}, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Empty(t, store.calls, "a workflow with no non-terminal status must never reach CountRecordsFiltered")
}

func TestCountOpenRecordsFiltered_PassesOpenFilterAndScopeThrough(t *testing.T) {
	store := &fakeOpenLeadsStore{count: 4}
	openIDs := []any{"1", "2"}
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	n, err := countOpenRecordsFiltered(context.Background(), store, nil, "lead", "own", "identity-1", openIDs, since, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 4, n)

	require.Len(t, store.calls, 1)
	call := store.calls[0]
	assert.Equal(t, "lead", call.key)
	assert.Equal(t, "own", call.scope)
	assert.Equal(t, "identity-1", call.actorIdentityID)
	assert.Equal(t, openRecordFilters(openIDs, since, time.Time{}), call.filters)
}

func TestCountOpenRecordsFiltered_PropagatesStoreError(t *testing.T) {
	boom := errors.New("boom")
	store := &fakeOpenLeadsStore{err: boom}
	_, err := countOpenRecordsFiltered(context.Background(), store, nil, "lead", "own", "identity-1", []any{"1"}, time.Time{}, time.Time{})
	assert.ErrorIs(t, err, boom)
}

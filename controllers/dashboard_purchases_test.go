package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/requisition"
)

func TestPurchasesStatus_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/purchases-status/data", nil)
	rr := httptest.NewRecorder()
	h.PurchasesStatus(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestPurchasesStatus_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/purchases-status/data", nil)
	rr := httptest.NewRecorder()
	h.PurchasesStatus(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestRequisitionParty(t *testing.T) {
	tests := []struct {
		name, vendor, department, want string
	}{
		{"vendor present wins", "Apex Stone Supply", "Fabrication", "Apex Stone Supply"},
		{"blank vendor falls back to department", "", "Fabrication", "Fabrication"},
		{"both blank falls back to a literal label", "", "", "Requisition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requisitionParty(tt.vendor, tt.department); got != tt.want {
				t.Errorf("requisitionParty(%q, %q) = %q, want %q", tt.vendor, tt.department, got, tt.want)
			}
		})
	}
}

func attentionIDs(rows []attentionRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func TestMergePendingByAge(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	pos := []purchaseorder.PendingApprovalPO{
		{ID: "po-old", RecordNumber: "PORD-1", Vendor: "Apex", Value: 100, CreatedAt: now.AddDate(0, 0, -6)},
		{ID: "po-new", RecordNumber: "PORD-2", Vendor: "Apex", Value: 200, CreatedAt: now.AddDate(0, 0, -1)},
	}
	reqns := []requisition.PendingApprovalREQN{
		{ID: "reqn-mid", RecordNumber: "REQN-1", Vendor: "", Department: "Fabrication", Value: 50, CreatedAt: now.AddDate(0, 0, -3)},
	}

	got := mergePendingByAge(pos, reqns, now)

	wantIDs := []string{"po-old", "reqn-mid", "po-new"}
	if len(got) != len(wantIDs) {
		t.Fatalf("len = %d, want %d (ids = %v)", len(got), len(wantIDs), attentionIDs(got))
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Errorf("got[%d].ID = %q, want %q (order was %v)", i, got[i].ID, want, attentionIDs(got))
		}
	}
	if got[0].Kind != "purchase_order" {
		t.Errorf("got[0].Kind = %q, want purchase_order", got[0].Kind)
	}
	if got[0].DaysWaiting == nil || *got[0].DaysWaiting != 6 {
		t.Errorf("got[0].DaysWaiting = %v, want 6", got[0].DaysWaiting)
	}
	if got[0].DaysOverdue != nil {
		t.Errorf("got[0].DaysOverdue = %v, want nil (this is a pending row, not an overdue one)", got[0].DaysOverdue)
	}
	if got[1].Kind != "requisition" || got[1].Party != "Fabrication" {
		t.Errorf("got[1] = %+v, want kind=requisition party=Fabrication (blank vendor falls back to department)", got[1])
	}
}

func TestMergeAttention_OverdueBeforePending(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	overdue := []purchaseorder.OverduePO{
		{ID: "po-overdue", RecordNumber: "PORD-9", Vendor: "Apex", Value: 6200, DaysOverdue: 3},
	}
	pendingPO := []purchaseorder.PendingApprovalPO{
		{ID: "po-pending", RecordNumber: "PORD-10", Vendor: "Apex", Value: 1000, CreatedAt: now.AddDate(0, 0, -6)},
	}

	got := mergeAttention(overdue, pendingPO, nil, now, 4)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (ids = %v)", len(got), attentionIDs(got))
	}
	if got[0].ID != "po-overdue" || got[0].DaysOverdue == nil || *got[0].DaysOverdue != 3 {
		t.Errorf("got[0] = %+v, want the overdue row first with DaysOverdue=3", got[0])
	}
	if got[1].ID != "po-pending" || got[1].DaysWaiting == nil {
		t.Errorf("got[1] = %+v, want the pending row second with DaysWaiting set", got[1])
	}
}

func TestMergeAttention_TruncatesAtLimit(t *testing.T) {
	now := time.Now()
	overdue := []purchaseorder.OverduePO{
		{ID: "a", DaysOverdue: 5}, {ID: "b", DaysOverdue: 4}, {ID: "c", DaysOverdue: 3},
	}
	pendingPO := []purchaseorder.PendingApprovalPO{
		{ID: "d", CreatedAt: now.AddDate(0, 0, -2)},
	}

	got := mergeAttention(overdue, pendingPO, nil, now, 2)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (capped at limit)", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("ids = %v, want [a b] (overdue rows fill the cap before any pending row is considered)", attentionIDs(got))
	}
}

func TestMergeAttention_EmptyBothSources(t *testing.T) {
	got := mergeAttention(nil, nil, nil, time.Now(), 4)
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestBuildPurchasesStatus_Granted(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		return purchaseorder.OpenTotals{IncomingCount: 3, IncomingValue: 4120, OverdueCount: 1, OverdueValue: 6200},
			[]purchaseorder.OverduePO{{ID: "po-1", RecordNumber: "PORD-9", Vendor: "Apex", Value: 6200, DaysOverdue: 3}}, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) {
		return purchaseorder.PendingApprovalResult{
			Eligible: true, TotalCount: 1, TotalValue: 1000,
			Rows: []purchaseorder.PendingApprovalPO{
				{ID: "po-2", RecordNumber: "PORD-10", Vendor: "Apex", Value: 1000, CreatedAt: now.AddDate(0, 0, -2)},
			},
		}, nil
	}
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) {
		return requisition.PendingApprovalResult{
			Eligible: true, TotalCount: 1, TotalValue: 1250,
			Rows: []requisition.PendingApprovalREQN{
				{ID: "reqn-1", RecordNumber: "REQN-121", Department: "Fabrication", Value: 1250, CreatedAt: now.AddDate(0, 0, -6)},
			},
		}, nil
	}

	result, ok, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, now, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if result.Incoming.Count != 3 || result.Incoming.Value != 4120 {
		t.Errorf("Incoming = %+v, want count=3 value=4120", result.Incoming)
	}
	if result.Overdue.Count != 1 || result.Overdue.Value != 6200 {
		t.Errorf("Overdue = %+v, want count=1 value=6200", result.Overdue)
	}
	if result.Pending == nil || result.Pending.Count != 2 || result.Pending.Value != 2250 {
		t.Errorf("Pending = %+v, want count=2 value=2250 (summed across both eligible modules)", result.Pending)
	}
	if result.AttentionCount != 3 {
		t.Errorf("AttentionCount = %d, want 3 (1 overdue + 1 PO pending + 1 reqn pending)", result.AttentionCount)
	}
	wantIDs := []string{"po-1", "reqn-1", "po-2"}
	if len(result.Attention) != len(wantIDs) {
		t.Fatalf("len(Attention) = %d, want %d (ids = %v)", len(result.Attention), len(wantIDs), attentionIDs(result.Attention))
	}
	for i, want := range wantIDs {
		if result.Attention[i].ID != want {
			t.Errorf("Attention[%d].ID = %q, want %q (overdue first, then pending oldest-first)", i, result.Attention[i].ID, want)
		}
	}
}

func TestBuildPurchasesStatus_PendingNullWhenIneligibleForBoth(t *testing.T) {
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		return purchaseorder.OpenTotals{}, nil, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) {
		return purchaseorder.PendingApprovalResult{Eligible: false}, nil
	}
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) {
		return requisition.PendingApprovalResult{Eligible: false}, nil
	}

	result, ok, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if result.Pending != nil {
		t.Errorf("Pending = %+v, want nil (caller is not a configured approver for either module)", result.Pending)
	}
}

func TestBuildPurchasesStatus_PendingCountsOnlyEligibleModule(t *testing.T) {
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		return purchaseorder.OpenTotals{}, nil, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) {
		return purchaseorder.PendingApprovalResult{Eligible: true, TotalCount: 2, TotalValue: 500}, nil
	}
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) {
		return requisition.PendingApprovalResult{Eligible: false, TotalCount: 99, TotalValue: 99999}, nil
	}

	result, ok, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if result.Pending == nil || result.Pending.Count != 2 || result.Pending.Value != 500 {
		t.Errorf("Pending = %+v, want count=2 value=500 (the ineligible module's numbers must not leak in)", result.Pending)
	}
}

func TestBuildPurchasesStatus_NotGranted(t *testing.T) {
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: false}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		t.Fatal("fetchOpen should not be called when the caller holds no grant")
		return purchaseorder.OpenTotals{}, nil, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) {
		t.Fatal("fetchPendingPO should not be called when the caller holds no grant")
		return purchaseorder.PendingApprovalResult{}, nil
	}
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) {
		t.Fatal("fetchPendingReqn should not be called when the caller holds no grant")
		return requisition.PendingApprovalResult{}, nil
	}

	_, ok, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildPurchasesStatus_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	checkPO := func() (authz.Decision, error) { return authz.Decision{}, boom }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		t.Fatal("fetchOpen should not be called when check errors")
		return purchaseorder.OpenTotals{}, nil, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) { return purchaseorder.PendingApprovalResult{}, nil }
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) { return requisition.PendingApprovalResult{}, nil }

	_, _, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildPurchasesStatus_PropagatesFetchOpenError(t *testing.T) {
	boom := errors.New("boom")
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		return purchaseorder.OpenTotals{}, nil, boom
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) {
		t.Fatal("fetchPendingPO should not be called when fetchOpen errors")
		return purchaseorder.PendingApprovalResult{}, nil
	}
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) {
		t.Fatal("fetchPendingReqn should not be called when fetchOpen errors")
		return requisition.PendingApprovalResult{}, nil
	}

	_, _, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildPurchasesStatus_PropagatesFetchPendingPOError(t *testing.T) {
	boom := errors.New("boom")
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		return purchaseorder.OpenTotals{}, nil, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) {
		return purchaseorder.PendingApprovalResult{}, boom
	}
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) {
		t.Fatal("fetchPendingReqn should not be called when fetchPendingPO errors")
		return requisition.PendingApprovalResult{}, nil
	}

	_, _, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildPurchasesStatus_PropagatesFetchPendingReqnError(t *testing.T) {
	boom := errors.New("boom")
	checkPO := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchOpen := func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
		return purchaseorder.OpenTotals{}, nil, nil
	}
	fetchPendingPO := func() (purchaseorder.PendingApprovalResult, error) { return purchaseorder.PendingApprovalResult{}, nil }
	fetchPendingReqn := func() (requisition.PendingApprovalResult, error) { return requisition.PendingApprovalResult{}, boom }

	_, _, err := buildPurchasesStatus(checkPO, fetchOpen, fetchPendingPO, fetchPendingReqn, time.Now(), 4)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

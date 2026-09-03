package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/requisition"
	"stonesuite-backend/workflow"
)

// purchasesAttentionLimit is how many rows the Purchases & requisitions
// status widget's attention worklist shows, and also how many rows each of
// its three underlying queries (overdue purchase orders, pending-approval
// purchase orders, pending-approval requisitions) fetches -- in the worst
// case every shown row could come from a single source, so each source
// must independently be able to supply the full limit for the post-merge
// truncation in mergeAttention to be correct.
const purchasesAttentionLimit = 4

// moneyCount is a tile's raw count + value pair, shared by the Incoming,
// Overdue and Pending tiles.
type moneyCount struct {
	Count int     `json:"count"`
	Value float64 `json:"value"`
}

// attentionRow is one row in the widget's combined worklist. Kind
// discriminates what kind of record this is ("purchase_order" or
// "requisition", for the frontend's link target and chip); exactly one of
// DaysOverdue/DaysWaiting is set, discriminating *why* the row is here --
// an overdue receipt (a promise already broken) vs. a pending approval (a
// promise not yet made). A purchase order can appear for either reason;
// day counts are raw ints, not pre-formatted strings -- formatting stays
// client-side (see the per-widget backend pattern in
// project_dashboard_widgets_rollout memory).
type attentionRow struct {
	Kind         string  `json:"kind"`
	ID           string  `json:"id"`
	RecordNumber string  `json:"recordNumber"`
	Party        string  `json:"party"`
	Value        float64 `json:"value"`
	DaysOverdue  *int    `json:"daysOverdue"`
	DaysWaiting  *int    `json:"daysWaiting"`
}

// purchasesStatusResult is the widget's fully-summarized payload. Pending
// is nil when the caller is not a configured approver for either purchase
// orders or requisitions (see buildPurchasesStatus) -- "not applicable to
// you", distinct from a real 0 ("you're caught up").
type purchasesStatusResult struct {
	Incoming       moneyCount
	Overdue        moneyCount
	Pending        *moneyCount
	Attention      []attentionRow
	AttentionCount int
}

// requisitionParty picks the display name for a requisition's attention
// row: its suggested vendor when set (AD-2 -- vendor is a nullable
// suggestion, not every requisition has one), else its free-text
// department, else a literal fallback so the row is never blank.
func requisitionParty(vendor, department string) string {
	if vendor != "" {
		return vendor
	}
	if department != "" {
		return department
	}
	return "Requisition"
}

// daysBetween is the whole-day count from then to now, floored -- used for
// "how long has this been waiting" on pending-approval rows, mirroring
// dashboard_kpi.go's own oldestDays computation (now.Sub(oldest).Hours()/24)
// for the KPI strip's Needs Approval tile.
func daysBetween(now, then time.Time) int {
	return int(now.Sub(then).Hours() / 24)
}

func poToAttentionRow(p purchaseorder.PendingApprovalPO, now time.Time) attentionRow {
	days := daysBetween(now, p.CreatedAt)
	return attentionRow{
		Kind: "purchase_order", ID: p.ID, RecordNumber: p.RecordNumber,
		Party: p.Vendor, Value: p.Value, DaysWaiting: &days,
	}
}

func reqnToAttentionRow(r requisition.PendingApprovalREQN, now time.Time) attentionRow {
	days := daysBetween(now, r.CreatedAt)
	return attentionRow{
		Kind: "requisition", ID: r.ID, RecordNumber: r.RecordNumber,
		Party: requisitionParty(r.Vendor, r.Department), Value: r.Value, DaysWaiting: &days,
	}
}

// mergePendingByAge merges two already oldest-first-sorted pending-approval
// lists (purchase orders and requisitions) into one oldest-first list of
// attention rows -- a standard two-pointer merge step, since each input is
// already sorted by the domain query's own ORDER BY.
func mergePendingByAge(pos []purchaseorder.PendingApprovalPO, reqns []requisition.PendingApprovalREQN, now time.Time) []attentionRow {
	out := make([]attentionRow, 0, len(pos)+len(reqns))
	i, j := 0, 0
	for i < len(pos) && j < len(reqns) {
		if pos[i].CreatedAt.Before(reqns[j].CreatedAt) {
			out = append(out, poToAttentionRow(pos[i], now))
			i++
		} else {
			out = append(out, reqnToAttentionRow(reqns[j], now))
			j++
		}
	}
	for ; i < len(pos); i++ {
		out = append(out, poToAttentionRow(pos[i], now))
	}
	for ; j < len(reqns); j++ {
		out = append(out, reqnToAttentionRow(reqns[j], now))
	}
	return out
}

// mergeAttention combines the widget's two kinds of "needs a human" rows
// into one worklist capped at limit: every overdue purchase order first
// (most overdue first, as already ordered by the domain query), then
// pending approvals merged oldest-waiting-first across both modules.
// Overdue receipts always rank above pending approvals regardless of how
// long either has waited -- a financial promise already broken (goods that
// were supposed to arrive and didn't) outranks one that hasn't been broken
// yet (a document still waiting for sign-off).
func mergeAttention(overdue []purchaseorder.OverduePO, pendingPO []purchaseorder.PendingApprovalPO, pendingReqn []requisition.PendingApprovalREQN, now time.Time, limit int) []attentionRow {
	out := make([]attentionRow, 0, limit)
	for _, o := range overdue {
		if len(out) >= limit {
			return out
		}
		days := o.DaysOverdue
		out = append(out, attentionRow{
			Kind: "purchase_order", ID: o.ID, RecordNumber: o.RecordNumber,
			Party: o.Vendor, Value: o.Value, DaysOverdue: &days,
		})
	}
	if len(out) >= limit {
		return out
	}

	for _, p := range mergePendingByAge(pendingPO, pendingReqn, now) {
		if len(out) >= limit {
			break
		}
		out = append(out, p)
	}
	return out
}

// buildPurchasesStatus resolves the Purchases & requisitions status
// widget's data given four injectable dependencies, so the summarization/
// merging is testable without a real tenant pool (see
// dashboard_purchases_test.go). checkPO gates the entire widget (purchase
// order visibility is the widget's mandatory resource, mirroring
// buildTopCustomers' invoice:read) -- ok=false (403) when denied, and
// neither fetchOpen nor either pending fetch is called. The two pending
// fetches are NOT gated by checkPO at all: like the KPI strip's Needs
// Approval tile (see controllers.approvalModuleCountAndOldest's doc
// comment), approval-queue visibility is authorized by approver-table
// membership, independent of RBAC read -- a caller who can read purchase
// orders but isn't a configured approver anywhere in purchasing simply
// gets Pending=nil, never a 403.
func buildPurchasesStatus(
	checkPO func() (authz.Decision, error),
	fetchOpen func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error),
	fetchPendingPO func() (purchaseorder.PendingApprovalResult, error),
	fetchPendingReqn func() (requisition.PendingApprovalResult, error),
	now time.Time,
	attentionLimit int,
) (purchasesStatusResult, bool, error) {
	decision, err := checkPO()
	if err != nil {
		return purchasesStatusResult{}, false, err
	}
	if !decision.Allowed {
		return purchasesStatusResult{}, false, nil
	}

	totals, overdueRows, err := fetchOpen(decision.Scope)
	if err != nil {
		return purchasesStatusResult{}, false, err
	}

	pendingPO, err := fetchPendingPO()
	if err != nil {
		return purchasesStatusResult{}, false, err
	}
	pendingReqn, err := fetchPendingReqn()
	if err != nil {
		return purchasesStatusResult{}, false, err
	}

	result := purchasesStatusResult{
		Incoming:       moneyCount{Count: totals.IncomingCount, Value: totals.IncomingValue},
		Overdue:        moneyCount{Count: totals.OverdueCount, Value: totals.OverdueValue},
		AttentionCount: totals.OverdueCount,
	}

	poRows, reqnRows := []purchaseorder.PendingApprovalPO{}, []requisition.PendingApprovalREQN{}
	if pendingPO.Eligible || pendingReqn.Eligible {
		pending := moneyCount{}
		if pendingPO.Eligible {
			pending.Count += pendingPO.TotalCount
			pending.Value += pendingPO.TotalValue
			result.AttentionCount += pendingPO.TotalCount
			poRows = pendingPO.Rows
		}
		if pendingReqn.Eligible {
			pending.Count += pendingReqn.TotalCount
			pending.Value += pendingReqn.TotalValue
			result.AttentionCount += pendingReqn.TotalCount
			reqnRows = pendingReqn.Rows
		}
		result.Pending = &pending
	}

	result.Attention = mergeAttention(overdueRows, poRows, reqnRows, now, attentionLimit)
	return result, true, nil
}

// PurchasesStatus serves the Purchases & requisitions status widget's data:
// how much is on order and not yet received (and how much of that is
// overdue), how much is stuck waiting for approval sign-off across
// purchase orders and requisitions, and a combined worklist of the records
// most needing a human. Ignores range -- every figure here is "right now"
// open work (how much is overdue, how long something has waited), and
// date-filtering would hide exactly the oldest, most-in-need-of-attention
// rows the widget exists to surface (mirrors inventory-alerts' same
// choice); range is still validated for contract uniformity with every
// other widget.
// GET /api/tenant/dashboard/widgets/purchases-status/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) PurchasesStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	rawRange := r.URL.Query().Get("range")
	now := time.Now()
	if _, ok := parseDashboardRange(rawRange, now); !ok {
		fail(w, http.StatusBadRequest, "range must be one of all, 7d, 30d, quarter.")
		return
	}
	_, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	isSuperAdmin, err := authz.IsSuperAdmin(ctx, pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load purchases status.")
		return
	}
	employeeID, foundEmployee := workflow.EmployeeIDByIdentity(ctx, pool, payload.ID)

	result, ok, err := buildPurchasesStatus(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourcePurchaseOrder, authz.ActionRead)
		},
		func(scope authz.Scope) (purchaseorder.OpenTotals, []purchaseorder.OverduePO, error) {
			return purchaseorder.DashboardOpen(ctx, pool, string(scope), payload.ID, purchasesAttentionLimit)
		},
		func() (purchaseorder.PendingApprovalResult, error) {
			if !foundEmployee && !isSuperAdmin {
				return purchaseorder.PendingApprovalResult{}, nil
			}
			return purchaseorder.DashboardPendingApproval(ctx, pool, employeeID, isSuperAdmin, purchasesAttentionLimit)
		},
		func() (requisition.PendingApprovalResult, error) {
			if !foundEmployee && !isSuperAdmin {
				return requisition.PendingApprovalResult{}, nil
			}
			return requisition.DashboardPendingApproval(ctx, pool, employeeID, isSuperAdmin, purchasesAttentionLimit)
		},
		now,
		purchasesAttentionLimit,
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load purchases status.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourcePurchaseOrder), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read purchase orders.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"range":          rangeLabel,
		"incoming":       result.Incoming,
		"overdue":        result.Overdue,
		"pending":        result.Pending,
		"attention":      result.Attention,
		"attentionCount": result.AttentionCount,
	})
}

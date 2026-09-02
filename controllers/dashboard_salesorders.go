package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/salesorder"
)

// salesOrdersAtRiskLimit is how many rows the Sales orders snapshot widget's
// at-risk worklist shows -- the most overdue-or-soonest-due open orders,
// ties broken by value (see salesorder.DashboardSnapshot).
const salesOrdersAtRiskLimit = 4

// statusBucketOut / atRiskOrderOut are this widget's JSON row shapes,
// distinct from salesorder.StatusBucket/AtRiskOrder (raw query results) so
// JSON tags stay this handler's own concern -- mirrors recentRecord's
// separation from each domain package's own response type.
type statusBucketOut struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Count int     `json:"count"`
	Value float64 `json:"value"`
}

type atRiskOrderOut struct {
	ID           string  `json:"id"`
	RecordNumber string  `json:"recordNumber"`
	Customer     string  `json:"customer"`
	Value        float64 `json:"value"`
	Status       string  `json:"status"`
	DaysLate     *int    `json:"daysLate"`
}

// salesOrdersSnapshotResult is the widget's fully-summarized payload.
type salesOrdersSnapshotResult struct {
	OpenCount int
	OpenValue float64
	LateCount int
	LateValue float64
	Statuses  []statusBucketOut
	AtRisk    []atRiskOrderOut
}

// summarizeOpenBacklog sums committed-backlog totals (open count/value, late
// count/value) from every non-terminal status bucket, counting only the
// statuses in openCodes toward the totals. Draft and Pending approval still
// appear in the returned Statuses breakdown -- so they're never invisible --
// but are excluded from OpenCount/OpenValue/LateCount/LateValue, since an
// unapproved order is not yet a customer commitment and shouldn't inflate
// the backlog or "at risk" figures the widget leads with.
func summarizeOpenBacklog(buckets []salesorder.StatusBucket, openCodes []string) salesOrdersSnapshotResult {
	open := make(map[string]bool, len(openCodes))
	for _, c := range openCodes {
		open[c] = true
	}

	out := salesOrdersSnapshotResult{Statuses: make([]statusBucketOut, 0, len(buckets))}
	for _, b := range buckets {
		out.Statuses = append(out.Statuses, statusBucketOut{Code: b.Code, Label: b.Label, Count: b.Count, Value: b.Value})
		if !open[b.Code] {
			continue
		}
		out.OpenCount += b.Count
		out.OpenValue += b.Value
		out.LateCount += b.LateCount
		out.LateValue += b.LateValue
	}
	return out
}

// mapAtRisk converts salesorder.AtRiskOrder query rows into the widget's
// JSON row shape.
func mapAtRisk(rows []salesorder.AtRiskOrder) []atRiskOrderOut {
	out := make([]atRiskOrderOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, atRiskOrderOut{
			ID:           r.ID,
			RecordNumber: r.RecordNumber,
			Customer:     r.Customer,
			Value:        r.Value,
			Status:       r.Status,
			DaysLate:     r.DaysLate,
		})
	}
	return out
}

// buildSalesOrdersSnapshot resolves the Sales orders snapshot widget's data
// given two injectable dependencies -- check (the caller's authz decision on
// sales orders) and fetch (the raw DB snapshot for a resolved scope) -- so
// the summarization/mapping is testable without a real tenant pool (see
// dashboard_salesorders_test.go). The HTTP handler wires check to
// authz.Check and fetch to salesorder.DashboardSnapshot.
//
// ok=false when the caller holds no sales_order:read grant at all; the
// handler maps that to 403. Unlike Pipeline mix/Recent records (which merge
// several independently-gated sources), this widget has exactly one source,
// so there is no partial-grant case to skip -- either the caller can read
// sales orders or they can't.
func buildSalesOrdersSnapshot(
	check func() (authz.Decision, error),
	fetch func(scope authz.Scope) (salesorder.SnapshotResult, error),
	openCodes []string,
) (salesOrdersSnapshotResult, bool, error) {
	decision, err := check()
	if err != nil {
		return salesOrdersSnapshotResult{}, false, err
	}
	if !decision.Allowed {
		return salesOrdersSnapshotResult{}, false, nil
	}

	snap, err := fetch(decision.Scope)
	if err != nil {
		return salesOrdersSnapshotResult{}, false, err
	}

	result := summarizeOpenBacklog(snap.Statuses, openCodes)
	result.AtRisk = mapAtRisk(snap.AtRisk)
	return result, true, nil
}

// SalesOrdersSnapshot serves the Sales orders snapshot widget's data:
// committed backlog count/value, how much of it is overdue, a live status
// breakdown (Draft through Partial), and a worklist of the most at-risk open
// orders. No dashboard_widget permission gate -- like the other widgets,
// gated entirely by the caller's own sales_order:read grant.
// GET /api/tenant/dashboard/widgets/sales-orders-snapshot/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) SalesOrdersSnapshot(w http.ResponseWriter, r *http.Request) {
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
	since, ok := parseDashboardRange(rawRange, time.Now())
	if !ok {
		fail(w, http.StatusBadRequest, "range must be one of all, 7d, 30d, quarter.")
		return
	}
	_, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	result, ok, err := buildSalesOrdersSnapshot(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourceSalesOrder, authz.ActionRead)
		},
		func(scope authz.Scope) (salesorder.SnapshotResult, error) {
			return salesorder.DashboardSnapshot(ctx, pool, string(scope), payload.ID, since, salesOrdersAtRiskLimit)
		},
		salesorder.OpenStatusCodes,
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load sales orders snapshot.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceSalesOrder), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read sales orders.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"range":     rangeLabel,
		"openCount": result.OpenCount,
		"openValue": result.OpenValue,
		"lateCount": result.LateCount,
		"lateValue": result.LateValue,
		"statuses":  result.Statuses,
		"atRisk":    result.AtRisk,
	})
}

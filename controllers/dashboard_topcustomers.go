package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
)

// topCustomersLimit is how many rows the Top customers widget's leaderboard
// shows.
const topCustomersLimit = 5

// topCustomerOut is this widget's JSON row shape, distinct from
// invoice.TopCustomer (raw query result) -- mirrors recentRecord/
// atRiskOrderOut's separation from each domain package's own type. ID is
// nil when the caller holds no customer:read grant (so the frontend renders
// the name unlinked rather than routing into a permission wall); PriorValue
// is nil when the selected range has no meaningful prior period to compare
// against ("all" time), and a real number (including 0) otherwise.
type topCustomerOut struct {
	ID         *string  `json:"id"`
	Name       string   `json:"name"`
	Value      float64  `json:"value"`
	PriorValue *float64 `json:"priorValue"`
}

// topCustomersResult is the widget's fully-mapped payload.
type topCustomersResult struct {
	Customers     []topCustomerOut
	TotalValue    float64
	CustomerCount int
}

// mapTopCustomers converts invoice.TopCustomer query rows into the widget's
// JSON row shape. prior is nil when there's no applicable prior window
// (every row's PriorValue is nil); otherwise a customer_uuid absent from
// prior reads as a real 0 (billed nothing last period), not nil -- see
// PriorRevenueByCustomer's doc comment.
func mapTopCustomers(rows []invoice.TopCustomer, prior map[string]float64, linkCustomerID bool) []topCustomerOut {
	out := make([]topCustomerOut, 0, len(rows))
	for _, r := range rows {
		c := topCustomerOut{Name: r.Name, Value: r.Value}
		if linkCustomerID {
			id := r.CustomerUUID
			c.ID = &id
		}
		if prior != nil {
			v := prior[r.CustomerUUID]
			c.PriorValue = &v
		}
		out = append(out, c)
	}
	return out
}

// buildTopCustomers resolves the Top customers widget's data given three
// injectable dependencies -- check (the caller's authz decision on
// invoices), fetchCurrent (the current-window ranking), and fetchPrior (the
// prior-window revenue for exactly the current window's customers) -- so
// the mapping/skip logic is testable without a real tenant pool (see
// dashboard_topcustomers_test.go). linkCustomerID is a plain bool rather
// than an injected check, since it only decides whether ID is populated in
// the output, never what data is fetched (see mapTopCustomers).
//
// fetchPrior is nil when the selected range has no applicable prior period
// (handler passes nil for "all") -- every row's PriorValue is then nil too.
// fetchPrior is also simply not called when the current window has no
// customers at all, since there is nothing to look up.
//
// ok=false when the caller holds no invoice:read grant; the handler maps
// that to 403.
func buildTopCustomers(
	check func() (authz.Decision, error),
	fetchCurrent func(scope authz.Scope) (invoice.TopCustomersResult, error),
	fetchPrior func(scope authz.Scope, customerUUIDs []string) (map[string]float64, error),
	linkCustomerID bool,
) (topCustomersResult, bool, error) {
	decision, err := check()
	if err != nil {
		return topCustomersResult{}, false, err
	}
	if !decision.Allowed {
		return topCustomersResult{}, false, nil
	}

	current, err := fetchCurrent(decision.Scope)
	if err != nil {
		return topCustomersResult{}, false, err
	}

	var prior map[string]float64
	if fetchPrior != nil && len(current.Customers) > 0 {
		uuids := make([]string, len(current.Customers))
		for i, c := range current.Customers {
			uuids[i] = c.CustomerUUID
		}
		prior, err = fetchPrior(decision.Scope, uuids)
		if err != nil {
			return topCustomersResult{}, false, err
		}
	}

	return topCustomersResult{
		Customers:     mapTopCustomers(current.Customers, prior, linkCustomerID),
		TotalValue:    current.TotalValue,
		CustomerCount: current.CustomerCount,
	}, true, nil
}

// TopCustomers serves the Top customers widget's data: the top
// topCustomersLimit customers ranked by billed invoice revenue in the
// selected range, each with a period-over-period comparison against the
// immediately preceding equal-length window (omitted on "all" time, which
// has no natural prior period), plus the total billed revenue and distinct
// customer count across every customer in the window (for the widget's
// concentration line). No dashboard_widget permission gate -- gated by the
// caller's own invoice:read grant, matching the other widgets.
// GET /api/tenant/dashboard/widgets/top-customers/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) TopCustomers(w http.ResponseWriter, r *http.Request) {
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
	now := time.Now()

	// customer:read is checked independently of the widget's main
	// invoice:read gate -- it only decides whether a row's id links to the
	// customer detail page, never whether the widget itself is visible.
	linkCustomerID := false
	if d, err := authz.Check(ctx, pool, payload.ID, authz.ResourceCustomer, authz.ActionRead); err == nil && d.Allowed {
		linkCustomerID = true
	}

	// A bounded range (7d/30d/quarter) gets a real prior-window comparison;
	// "all"/"" has no natural prior period, so fetchPrior stays nil and
	// every row's priorValue comes back nil (see buildTopCustomers).
	var fetchPrior func(scope authz.Scope, customerUUIDs []string) (map[string]float64, error)
	if rawRange != "" && rawRange != "all" {
		_, priorFrom, priorTo := dashboardDeltaWindow(rawRange, now)
		fetchPrior = func(scope authz.Scope, customerUUIDs []string) (map[string]float64, error) {
			return invoice.PriorRevenueByCustomer(ctx, pool, string(scope), payload.ID, customerUUIDs, priorFrom, priorTo)
		}
	}

	result, ok, err := buildTopCustomers(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourceInvoice, authz.ActionRead)
		},
		func(scope authz.Scope) (invoice.TopCustomersResult, error) {
			return invoice.TopCustomersByRevenue(ctx, pool, string(scope), payload.ID, since, topCustomersLimit)
		},
		fetchPrior,
		linkCustomerID,
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load top customers.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInvoice), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read invoices.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"range":         rangeLabel,
		"customers":     result.Customers,
		"totalValue":    result.TotalValue,
		"customerCount": result.CustomerCount,
	})
}

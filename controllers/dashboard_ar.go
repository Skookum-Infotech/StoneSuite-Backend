package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
)

// arOutstandingLimit is how many rows the A/R widget's oldest-outstanding
// worklist shows, matching topCustomersLimit/inventoryAlertsLimit so every
// half-width widget lists the same number of rows.
const arOutstandingLimit = 5

// agingBucketOut is one aging bar's JSON shape, distinct from
// invoice.AgingBucket (query result) -- mirrors topCustomerOut's separation
// from the domain package's own type.
type agingBucketOut struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

// outstandingInvoiceOut is one worklist row. ID is the invoice uuid, which
// the widget routes to; DaysPastDue is 0 for an invoice with no due date
// (see invoice.OutstandingAging's doc comment on why that isn't "overdue").
type outstandingInvoiceOut struct {
	ID          string  `json:"id"`
	Number      string  `json:"invoiceNumber"`
	Customer    string  `json:"customer"`
	BalanceDue  float64 `json:"balanceDue"`
	DaysPastDue int     `json:"daysPastDue"`
}

// arOutstandingResult is the widget's fully-mapped payload.
type arOutstandingResult struct {
	Outstanding  float64
	OverdueTotal float64
	OverdueCount int
	Buckets      []agingBucketOut
	Oldest       []outstandingInvoiceOut
	OldestCount  int
}

// mapAgingBuckets converts invoice.AgingBucket query rows into the widget's
// JSON shape. All four buckets are always present and in order -- see
// invoice.AgingBucketLabels.
func mapAgingBuckets(buckets []invoice.AgingBucket) []agingBucketOut {
	out := make([]agingBucketOut, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, agingBucketOut{Label: b.Label, Amount: b.Amount, Count: b.Count})
	}
	return out
}

// mapOutstandingInvoices converts invoice.OutstandingInvoice query rows into
// the widget's worklist row shape.
func mapOutstandingInvoices(rows []invoice.OutstandingInvoice) []outstandingInvoiceOut {
	out := make([]outstandingInvoiceOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, outstandingInvoiceOut{
			ID:          r.UUID,
			Number:      r.Number,
			Customer:    r.Customer,
			BalanceDue:  r.BalanceDue,
			DaysPastDue: r.DaysPastDue,
		})
	}
	return out
}

// buildArOutstanding resolves the A/R widget's data given three injectable
// dependencies -- check (the caller's authz decision on invoices),
// fetchAging (the tenant-wide aging aggregate) and fetchOldest (the worklist
// rows) -- so the gating and mapping logic is testable without a real tenant
// pool (see dashboard_ar_test.go).
//
// fetchOldest is skipped entirely when nothing is outstanding, since there is
// no worklist to build.
//
// ok=false when the caller holds no invoice:read grant; the handler maps that
// to 403.
func buildArOutstanding(
	check func() (authz.Decision, error),
	fetchAging func(scope authz.Scope) (invoice.AgingResult, error),
	fetchOldest func(scope authz.Scope) ([]invoice.OutstandingInvoice, error),
) (arOutstandingResult, bool, error) {
	decision, err := check()
	if err != nil {
		return arOutstandingResult{}, false, err
	}
	if !decision.Allowed {
		return arOutstandingResult{}, false, nil
	}

	aging, err := fetchAging(decision.Scope)
	if err != nil {
		return arOutstandingResult{}, false, err
	}

	result := arOutstandingResult{
		Outstanding:  aging.Outstanding,
		OverdueTotal: aging.OverdueTotal,
		OverdueCount: aging.OverdueCount,
		Buckets:      mapAgingBuckets(aging.Buckets),
		Oldest:       []outstandingInvoiceOut{},
		OldestCount:  aging.OutstandingCount,
	}
	if aging.OutstandingCount == 0 {
		return result, true, nil
	}

	oldest, err := fetchOldest(decision.Scope)
	if err != nil {
		return arOutstandingResult{}, false, err
	}
	result.Oldest = mapOutstandingInvoices(oldest)
	return result, true, nil
}

// ArOutstanding serves the Accounts receivable widget's data: the tenant's
// total outstanding invoice balance, how much of it is genuinely overdue, a
// four-bucket aging breakdown, and the arOutstandingLimit invoices furthest
// past due. No dashboard_widget permission gate -- gated by the caller's own
// invoice:read grant, matching the other widgets.
//
// Like inventory-alerts, this widget ignores `range`: an outstanding balance
// is current state, not a date window. The parameter is still validated and
// echoed back for contract uniformity with the range-aware widgets.
// GET /api/tenant/dashboard/widgets/ar-outstanding/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) ArOutstanding(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := parseDashboardRange(rawRange, time.Now()); !ok {
		fail(w, http.StatusBadRequest, "range must be one of all, 7d, 30d, quarter.")
		return
	}
	_, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	result, ok, err := buildArOutstanding(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourceInvoice, authz.ActionRead)
		},
		func(scope authz.Scope) (invoice.AgingResult, error) {
			return invoice.OutstandingAging(ctx, pool, string(scope), payload.ID)
		},
		func(scope authz.Scope) ([]invoice.OutstandingInvoice, error) {
			return invoice.OldestOutstanding(ctx, pool, string(scope), payload.ID, arOutstandingLimit)
		},
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load accounts receivable.")
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
		"success":      true,
		"range":        rangeLabel,
		"outstanding":  result.Outstanding,
		"overdueTotal": result.OverdueTotal,
		"overdueCount": result.OverdueCount,
		"buckets":      result.Buckets,
		"oldest":       result.Oldest,
		"oldestCount":  result.OldestCount,
	})
}

package controllers

import (
	"errors"
	"net/http"
	"time"

	"stonesuite-backend/accountingperiod"
	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/middleware"
)

// accountingSnapshotLimit is how many recent entries the Accounting snapshot
// widget lists, matching the other half-width widgets' row count.
const accountingSnapshotLimit = 5

// accountingPeriodOut is the widget's period pill. Nil on the payload
// entirely when the tenant has never configured an accounting calendar --
// the widget then renders a "set up your calendar" empty state rather than
// inventing a month.
type accountingPeriodOut struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	EntryCount int    `json:"entryCount"`
}

// journalEntryOut is one recent-entry row. Date is a real timestamp, not a
// pre-formatted string -- the widget renders "2h ago" itself, in the viewer's
// own locale (see the frontend's relativeTime helper).
type journalEntryOut struct {
	ID          string    `json:"id"`
	Number      string    `json:"entryNumber"`
	Description string    `json:"description"`
	Amount      float64   `json:"amount"`
	Date        time.Time `json:"date"`
}

// accountingSnapshotResult is the widget's fully-mapped payload.
type accountingSnapshotResult struct {
	Period     *accountingPeriodOut
	Entries    []journalEntryOut
	EntryTotal int
}

// mapRecentEntries converts cashtransfer.RecentEntry query rows into the
// widget's JSON row shape.
func mapRecentEntries(rows []cashtransfer.RecentEntry) []journalEntryOut {
	out := make([]journalEntryOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, journalEntryOut{
			ID:          r.UUID,
			Number:      r.Number,
			Description: r.Description,
			Amount:      r.Amount,
			Date:        r.Date,
		})
	}
	return out
}

// buildAccountingSnapshot resolves the Accounting snapshot widget's data
// given four injectable dependencies -- check (the caller's authz decision on
// journal entries), fetchPeriod (the period covering today), countEntries
// (how many entries that period holds) and fetchRecent (the listed rows) --
// so the gating and mapping logic is testable without a real tenant pool
// (see dashboard_accounting_test.go).
//
// fetchPeriod returns a nil period when the tenant has no accounting calendar
// configured; countEntries is then not called at all, since there is no
// window to count over, and EntryTotal falls back to the number of listed
// rows so the widget's "N entries" line still says something true.
//
// ok=false when the caller holds no cash_transfer:read grant; the handler
// maps that to 403.
func buildAccountingSnapshot(
	check func() (authz.Decision, error),
	fetchPeriod func() (*accountingperiod.Period, error),
	countEntries func(scope authz.Scope, from, to time.Time) (int, error),
	fetchRecent func(scope authz.Scope) ([]cashtransfer.RecentEntry, error),
) (accountingSnapshotResult, bool, error) {
	decision, err := check()
	if err != nil {
		return accountingSnapshotResult{}, false, err
	}
	if !decision.Allowed {
		return accountingSnapshotResult{}, false, nil
	}

	period, err := fetchPeriod()
	if err != nil {
		return accountingSnapshotResult{}, false, err
	}

	recent, err := fetchRecent(decision.Scope)
	if err != nil {
		return accountingSnapshotResult{}, false, err
	}

	result := accountingSnapshotResult{
		Entries:    mapRecentEntries(recent),
		EntryTotal: len(recent),
	}
	if period == nil {
		return result, true, nil
	}

	count, err := countEntries(decision.Scope, period.Start, period.End)
	if err != nil {
		return accountingSnapshotResult{}, false, err
	}
	result.EntryTotal = count
	result.Period = &accountingPeriodOut{
		Name:       period.Name,
		Status:     period.Status,
		EntryCount: count,
	}
	return result, true, nil
}

// AccountingSnapshot serves the Accounting snapshot widget's data: the
// accounting period covering today with its open/closed state and entry
// count, plus the accountingSnapshotLimit most recent posted journal entries.
// No dashboard_widget permission gate -- gated by the caller's own
// cash_transfer:read grant, matching the other widgets.
//
// The period itself needs no separate grant: accounting periods are
// tenant-global master data with no owner column and no per-record scope
// (see accountingperiod's package doc), so there is nothing to leak.
//
// Like inventory-alerts, this widget ignores `range` -- the current period is
// defined by the tenant's calendar, not by the console's time window. The
// parameter is still validated and echoed back for contract uniformity.
// GET /api/tenant/dashboard/widgets/accounting-snapshot/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) AccountingSnapshot(w http.ResponseWriter, r *http.Request) {
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

	result, ok, err := buildAccountingSnapshot(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourceCashTransfer, authz.ActionRead)
		},
		func() (*accountingperiod.Period, error) {
			// A tenant that never ran calendar Setup has no period covering
			// today. That's an unconfigured tenant, not a failure.
			period, err := accountingperiod.Current(ctx, pool)
			if errors.Is(err, accountingperiod.ErrNotFound) {
				return nil, nil
			}
			return period, err
		},
		func(scope authz.Scope, from, to time.Time) (int, error) {
			return cashtransfer.CountBetween(ctx, pool, string(scope), payload.ID, from, to)
		},
		func(scope authz.Scope) ([]cashtransfer.RecentEntry, error) {
			return cashtransfer.RecentPosted(ctx, pool, string(scope), payload.ID, accountingSnapshotLimit)
		},
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load accounting snapshot.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCashTransfer), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read journal entries.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"range":      rangeLabel,
		"period":     result.Period,
		"entries":    result.Entries,
		"entryTotal": result.EntryTotal,
	})
}

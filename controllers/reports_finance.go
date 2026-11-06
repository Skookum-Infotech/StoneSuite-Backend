package controllers

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
	"stonesuite-backend/tenancy"
)

// reportWindow is a parsed inclusive from/to date window (zero = unbounded).
type reportWindow struct {
	from, to time.Time
}

// authorizeFinanceReport runs the checks every Finance report shares: GET only,
// signed in, a valid from/to window, report_finance:read. The ledger has no
// per-row owner, so there is no data scope to carry.
func (h *ReportsOps) authorizeFinanceReport(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, reportWindow, bool) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return nil, reportWindow{}, false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, reportWindow{}, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, reportWindow{}, false
	}
	from, to, ok := parseReportWindow(w, r)
	if !ok {
		return nil, reportWindow{}, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReportFinance, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, reportWindow{}, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to view finance reports.")
		return nil, reportWindow{}, false
	}
	return pool, reportWindow{from: from, to: to}, true
}

// ProfitAndLoss serves Reports > Finance > Profit & Loss for journal entries
// dated in the window.
// GET /api/tenant/reports/finance/profit-loss?from=&to=
func (h *ReportsOps) ProfitAndLoss(w http.ResponseWriter, r *http.Request) {
	pool, win, ok := h.authorizeFinanceReport(w, r)
	if !ok {
		return
	}
	pl, err := reports.ProfitAndLoss(r.Context(), pool, win.from, win.to)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "report": pl})
}

// AccountLines serves the P&L drill-down: the journal lines behind one account's
// figure. accountId is the account's uuid.
// GET /api/tenant/reports/finance/account-lines?accountId=&from=&to=
func (h *ReportsOps) AccountLines(w http.ResponseWriter, r *http.Request) {
	pool, win, ok := h.authorizeFinanceReport(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("accountId")
	if !reports.IsUUID(id) {
		fail(w, http.StatusBadRequest, "accountId must be a valid id.")
		return
	}
	lines, err := reports.AccountLines(r.Context(), pool, id, win.from, win.to)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not load the journal lines.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "lines": lines})
}

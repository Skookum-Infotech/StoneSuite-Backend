package controllers

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
	"stonesuite-backend/tenancy"
)

// crmReportRequest is a parsed + authorized CRM report call.
type crmReportRequest struct {
	filter reports.CRMFilter
	pool   *pgxpool.Pool
}

// authorizeCRMReport runs the checks every CRM report shares: GET only, signed
// in, report_crm:read, and a tenant on the relational (V2) CRM design -- the
// reports read the relational customer table, which a V1 tenant does not have.
func (h *ReportsOps) authorizeCRMReport(w http.ResponseWriter, r *http.Request) (crmReportRequest, bool) {
	var req crmReportRequest
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return req, false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return req, false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return req, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return req, false
	}
	if tenant.DesignVersion != tenancy.DesignV2 {
		fail(w, http.StatusNotImplemented, "CRM reports are not available for this workspace's CRM design.")
		return req, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReportCRM, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return req, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to view CRM reports.")
		return req, false
	}
	return crmReportRequest{
		filter: reports.CRMFilter{Scope: decision.Scope, ActorIdentityID: payload.ID},
		pool:   pool,
	}, true
}

// parseCRMWindow reads the optional from/to lead-creation window.
func parseCRMWindow(w http.ResponseWriter, r *http.Request, f *reports.CRMFilter) bool {
	q := r.URL.Query()
	from, ok := parseReportDate(q.Get("from"))
	if !ok {
		fail(w, http.StatusBadRequest, "from must be a YYYY-MM-DD date.")
		return false
	}
	to, ok := parseReportDate(q.Get("to"))
	if !ok {
		fail(w, http.StatusBadRequest, "to must be a YYYY-MM-DD date.")
		return false
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		fail(w, http.StatusBadRequest, "to must not be before from.")
		return false
	}
	f.From, f.To = from, to
	return true
}

// ConversionFunnel serves Reports > CRM > Conversion Funnel: of the leads
// created in the window, how many qualified and became prospects and customers.
// GET /api/tenant/reports/crm/funnel?from=&to=
func (h *ReportsOps) ConversionFunnel(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCRMReport(w, r)
	if !ok || !parseCRMWindow(w, r, &req.filter) {
		return
	}
	funnel, err := reports.ConversionFunnel(r.Context(), req.pool, req.filter)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "stages": funnel.Stages, "unqualified": funnel.Unqualified})
}

// LeadSources serves Reports > CRM > Lead Source Performance.
// GET /api/tenant/reports/crm/lead-sources?from=&to=
func (h *ReportsOps) LeadSources(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCRMReport(w, r)
	if !ok || !parseCRMWindow(w, r, &req.filter) {
		return
	}
	rows, totals, err := reports.LeadSourcePerformance(r.Context(), req.pool, req.filter)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// StaleRecords serves Reports > CRM > Stale Leads & Prospects: open leads and
// prospects idle for at least `days` days (default 14).
// GET /api/tenant/reports/crm/stale?days=14
func (h *ReportsOps) StaleRecords(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCRMReport(w, r)
	if !ok {
		return
	}
	days := reports.DefaultStaleDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < reports.MinStaleDays || n > reports.MaxStaleDays {
			fail(w, http.StatusBadRequest, "days must be a whole number from "+strconv.Itoa(reports.MinStaleDays)+" to "+strconv.Itoa(reports.MaxStaleDays)+".")
			return
		}
		days = n
	}
	rows, err := reports.StaleRecords(r.Context(), req.pool, days, req.filter.Scope, req.filter.ActorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "days": days, "rows": rows})
}

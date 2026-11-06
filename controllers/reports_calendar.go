package controllers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
	"stonesuite-backend/tenancy"
)

// calendarReportRequest is a parsed + authorized Calendar report call.
type calendarReportRequest struct {
	pool            *pgxpool.Pool
	scope           authz.Scope
	actorIdentityID string
	from, to        time.Time
}

// authorizeCalendarReport runs the checks every Calendar report shares: GET
// only, signed in, report_calendar:read. withWindow also parses from/to.
func (h *ReportsOps) authorizeCalendarReport(w http.ResponseWriter, r *http.Request, withWindow bool) (calendarReportRequest, bool) {
	var req calendarReportRequest
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return req, false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return req, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return req, false
	}
	if withWindow {
		var ok bool
		if req.from, req.to, ok = parseReportWindow(w, r); !ok {
			return req, false
		}
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReportCalendar, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return req, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to view calendar reports.")
		return req, false
	}
	req.pool, req.scope, req.actorIdentityID = pool, decision.Scope, payload.ID
	return req, true
}

// UpcomingInstalls serves Reports > Calendar > Upcoming Installs.
// GET /api/tenant/reports/calendar/upcoming-installs?weeks=8
func (h *ReportsOps) UpcomingInstalls(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCalendarReport(w, r, false)
	if !ok {
		return
	}
	weeks := reports.DefaultUpcomingWeeks
	if raw := r.URL.Query().Get("weeks"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > reports.MaxUpcomingWeeks {
			fail(w, http.StatusBadRequest, "weeks must be a whole number from 1 to "+strconv.Itoa(reports.MaxUpcomingWeeks)+".")
			return
		}
		weeks = n
	}
	rows, perWeek, err := reports.UpcomingInstalls(r.Context(), req.pool, weeks, req.scope, req.actorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "weeks": weeks, "perWeek": perWeek, "rows": rows})
}

// OverdueActivities serves Reports > Calendar > Overdue Activities, as of now.
// GET /api/tenant/reports/calendar/overdue
func (h *ReportsOps) OverdueActivities(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCalendarReport(w, r, false)
	if !ok {
		return
	}
	rows, totals, err := reports.OverdueActivities(r.Context(), req.pool, req.scope, req.actorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// OnTimeCompletion serves Reports > Calendar > On-time Completion for a window
// of completion dates.
// GET /api/tenant/reports/calendar/on-time?from=&to=
func (h *ReportsOps) OnTimeCompletion(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCalendarReport(w, r, true)
	if !ok {
		return
	}
	rows, totals, err := reports.OnTimeCompletion(r.Context(), req.pool, req.from, req.to, req.scope, req.actorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// OrderToInstallLeadTime serves Reports > Calendar > Order-to-Install Lead Time
// for a window of install-completion dates.
// GET /api/tenant/reports/calendar/lead-time?from=&to=
func (h *ReportsOps) OrderToInstallLeadTime(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeCalendarReport(w, r, true)
	if !ok {
		return
	}
	rows, totals, err := reports.OrderToInstallLeadTime(r.Context(), req.pool, req.from, req.to, req.scope, req.actorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

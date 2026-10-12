package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
)

// errInstallDateRequired is the 400 for a create or update with no scheduled
// installation date. The store treats a blank date as "leave as is", so the
// requirement lives here, at the HTTP contract.
const errInstallDateRequired = "A scheduled installation date is required."

// ScheduleFeed GET /api/tenant/sales-orders/schedule?group=Template&from=2026-10-12&to=2026-10-18
// The calendar's feed: every planned activity of one group (Template, Saw, ...)
// across the orders the caller can read whose scheduled time falls in the
// window — both dates inclusive, at most 93 days. Cancelled orders are left
// out. Scoped like the order list: an own/team-scoped caller sees only the
// orders they own.
func (h *SalesOrderOps) ScheduleFeed(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authSO(w, r, authz.ActionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	res, err := salesorder.ScheduleFeed(r.Context(), pool, string(scope), identityID,
		q.Get("group"), q.Get("from"), q.Get("to"))
	if err != nil {
		soFail(w, err, "Failed to load the schedule calendar.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"scope":     scope,
		"events":    res.Events,
		"truncated": res.Truncated,
	})
}

// Schedule GET /api/tenant/sales-orders/{uuid}/schedule
// Returns the order's auto-planned production schedule in plan order — an
// empty list until an installation date has been set.
func (h *SalesOrderOps) Schedule(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	activities, err := salesorder.Schedule(r.Context(), pool, uuid)
	if err != nil {
		soFail(w, err, "Failed to load schedule.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "activities": activities})
}

// UpdateScheduleActivity PATCH /api/tenant/sales-orders/{uuid}/schedule/{code}
// body: {"status":"scheduled|completed","scheduledAt":"yyyy-mm-ddThh:mm",
// "completedAt":"yyyy-mm-ddThh:mm","notes":"..."} — every field optional.
// Gated on sales_order:update, like editing the order itself.
func (h *SalesOrderOps) UpdateScheduleActivity(w http.ResponseWriter, r *http.Request) {
	uuid, code := r.PathValue("uuid"), r.PathValue("code")
	pool, identityID, order, ok := h.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var patch salesorder.ActivityPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	activity, err := salesorder.UpdateActivity(r.Context(), pool, uuid, code, patch, resolveEmployeeID(r, identityID))
	if err != nil {
		soFail(w, err, "Failed to update schedule activity.")
		return
	}
	auditSOMeta(r, pool, identityID, "schedule_update", uuid, nil, order,
		map[string]any{"activity": code, "status": activity.Status})
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "activity": activity})
}

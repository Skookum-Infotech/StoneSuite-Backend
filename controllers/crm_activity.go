// controllers/crm_activity.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/crmactivity"
)

// CRMActivityOps handles the CRM activity log: a sub-resource of a CRM
// record (lead/prospect/customer). Every handler first resolves +
// IDOR-guards the parent record via CRMOps.authCRMByRecordID (requiring
// <parent-resource>:read — a caller must be able to see the customer to log
// or view activity against them), then separately checks
// crm_activity:<action> for the activity-specific operation, mirroring the
// two-permission (source + target) pattern used by the document conversion
// endpoints.
//
// Routes:
//
//	GET    /api/tenant/crm/{workflowKey}/records/{id}/activities                  — list
//	POST   /api/tenant/crm/{workflowKey}/records/{id}/activities                  — create
//	PATCH  /api/tenant/crm/{workflowKey}/records/{id}/activities/{activityId}     — update
//	DELETE /api/tenant/crm/{workflowKey}/records/{id}/activities/{activityId}     — soft delete
type CRMActivityOps struct {
	crm *CRMOps
}

// NewCRMActivityOps constructs the handler group.
func NewCRMActivityOps() *CRMActivityOps { return &CRMActivityOps{crm: NewCRMOps()} }

// authActivity resolves the parent CRM record (IDOR-guarded, read access
// required) then checks crm_activity:<action>.
func (h *CRMActivityOps) authActivity(w http.ResponseWriter, r *http.Request, recordID string, action authz.Action) (*pgxpool.Pool, string, bool) {
	_, pool, _, identityID, ok := h.crm.authCRMByRecordID(w, r, recordID, authz.ActionRead)
	if !ok {
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceCRMActivity, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceCRMActivity), "action", string(action),
			"parent_record", recordID)
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" activities.")
		return nil, "", false
	}
	return pool, identityID, true
}

// activityFail maps a store error to an HTTP response.
func activityFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, crmactivity.ErrNotFound):
		fail(w, http.StatusNotFound, "Activity not found.")
	case crmactivity.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// List GET /api/tenant/crm/{workflowKey}/records/{id}/activities?type=call
func (h *CRMActivityOps) List(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, ok := h.authActivity(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := crmactivity.List(r.Context(), pool, recordID, r.URL.Query().Get("type"))
	if err != nil {
		activityFail(w, err, "Failed to load activities.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": recordID, "activities": entries})
}

// Create POST /api/tenant/crm/{workflowKey}/records/{id}/activities
func (h *CRMActivityOps) Create(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, identityID, ok := h.authActivity(w, r, recordID, authz.ActionCreate)
	if !ok {
		return
	}
	var in crmactivity.CreateActivityInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	act, err := crmactivity.Create(r.Context(), pool, recordID, in, resolveEmployeeID(r, identityID))
	if err != nil {
		activityFail(w, err, "Failed to log activity.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "activity": act})
}

// Update PATCH /api/tenant/crm/{workflowKey}/records/{id}/activities/{activityId}
func (h *CRMActivityOps) Update(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	activityID := r.PathValue("activityId")
	pool, identityID, ok := h.authActivity(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	var in crmactivity.UpdateActivityInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	act, err := crmactivity.Update(r.Context(), pool, recordID, activityID, in, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, crmactivity.ErrNotFound) {
			// Either a genuinely unknown activityId, or (the security-relevant
			// case) one that belongs to a different CRM record than recordID —
			// verifyBelongsToRecord doesn't distinguish the two, so log both as
			// a possible IDOR probe rather than staying silent.
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", recordID, "resource", string(authz.ResourceCRMActivity),
				"action", string(authz.ActionUpdate), "activity", activityID)
		}
		activityFail(w, err, "Failed to update activity.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "activity": act})
}

// Delete DELETE /api/tenant/crm/{workflowKey}/records/{id}/activities/{activityId}
func (h *CRMActivityOps) Delete(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	activityID := r.PathValue("activityId")
	pool, identityID, ok := h.authActivity(w, r, recordID, authz.ActionDelete)
	if !ok {
		return
	}
	if err := crmactivity.SoftDelete(r.Context(), pool, recordID, activityID, resolveEmployeeID(r, identityID)); err != nil {
		if errors.Is(err, crmactivity.ErrNotFound) {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", recordID, "resource", string(authz.ResourceCRMActivity),
				"action", string(authz.ActionDelete), "activity", activityID)
		}
		activityFail(w, err, "Failed to delete activity.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Activity deleted."})
}

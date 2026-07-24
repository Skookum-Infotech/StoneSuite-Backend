package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// Transition PUT /api/tenant/fabrication-jobs/{uuid}/fabrication/status
// body {"toStatusCode":"CUTG"}. Cancellation is routed through Cancel so the
// disposition gate applies.
func (h *FabricationOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string `json:"toStatusCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	if req.ToStatusCode == fabrication.StatusCancelled {
		job, err := fabrication.Cancel(r.Context(), pool, uuid, empID)
		if err != nil {
			fjFail(w, err, "Failed to cancel fabrication job.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
		return
	}
	job, err := fabrication.Transition(r.Context(), pool, uuid, req.ToStatusCode, empID)
	if err != nil {
		fjFail(w, err, "Failed to apply transition.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
}

// Hold POST /api/tenant/fabrication-jobs/{uuid}/hold
func (h *FabricationOps) Hold(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	job, err := fabrication.Hold(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to place job on hold.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
}

// Resume POST /api/tenant/fabrication-jobs/{uuid}/resume — takes NO body: the
// resume target is stored on the row, never caller-supplied (spec §1.2).
func (h *FabricationOps) Resume(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	job, err := fabrication.Resume(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to resume job.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
}

// Approve POST /api/tenant/fabrication-jobs/{uuid}/approve — records the caller's
// sign-off at the current gate (TAPV/QCPS). A non-approver gets 403.
func (h *FabricationOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	job, err := fabrication.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if err == fabrication.ErrNotApprover {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		fjFail(w, err, "Failed to approve fabrication job.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
}

package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// Steps GET /api/tenant/fabrication-jobs/{uuid}/steps — the 16-step checklist.
func (h *FabricationOps) Steps(w http.ResponseWriter, r *http.Request) {
	_, _, job, ok := h.authFJByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "steps": job.Steps})
}

// UpdateStep PATCH /api/tenant/fabrication-jobs/{uuid}/steps/{stepCode}
// body {"status":"completed","notes":"...","payload":{...}}
func (h *FabricationOps) UpdateStep(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	stepCode := r.PathValue("stepCode")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var req struct {
		Status  string         `json:"status"`
		Notes   string         `json:"notes"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		fail(w, http.StatusBadRequest, "status is required.")
		return
	}
	step, err := fabrication.UpdateStep(r.Context(), pool, uuid, stepCode, req.Status, req.Notes, req.Payload, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to update step.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "step": step})
}

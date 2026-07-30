// controllers/cashtransfer_actions.go — Approve/Cancel (generic transition),
// Post, and Reverse. Split from cashtransfer.go for the 300-line file cap.
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
)

// Transition POST /api/tenant/finance/cash-transfers/{uuid}/transition  body {"toStatusCode":"..."}
// Used for Approve (DRFT->APPR) and Cancel (->CANC). Post and Reverse are
// their own dedicated endpoints below (spec AD-6).
func (h *CashTransferOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, uuid, authz.ActionTransition)
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
	ct, err := cashtransfer.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		ctFail(w, err, "Failed to apply transition.")
		return
	}
	auditCT(r, pool, resolveEmployeeID(r, identityID), "transition", uuid, nil, ct)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

// Post POST /api/tenant/finance/cash-transfers/{uuid}/post
func (h *CashTransferOps) Post(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	ct, err := cashtransfer.Post(r.Context(), pool, uuid, empID)
	if err != nil {
		ctFail(w, err, "Failed to post cash transfer.")
		return
	}
	auditCT(r, pool, empID, "post", uuid, nil, ct)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

// Reverse POST /api/tenant/finance/cash-transfers/{uuid}/reverse
func (h *CashTransferOps) Reverse(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	ct, err := cashtransfer.Reverse(r.Context(), pool, uuid, empID)
	if err != nil {
		ctFail(w, err, "Failed to reverse cash transfer.")
		return
	}
	auditCT(r, pool, empID, "reverse", uuid, nil, ct)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

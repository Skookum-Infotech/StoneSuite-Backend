package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

type vendorBillTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

// Transition handles POST /api/tenant/vendor-bills/{uuid}/transition.
func (h *VendorBillOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorBillByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var req vendorBillTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vb, err := vendorbill.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		vendorBillFail(w, err, "Failed to transition vendor bill.")
		return
	}
	auditVendorBill(r, pool, empID, "transition", id, nil, vb)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": vb})
}

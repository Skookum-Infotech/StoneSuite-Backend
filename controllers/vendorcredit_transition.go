// controllers/vendorcredit_transition.go
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorcredit"
)

// vcTransitionRequest is the body of POST .../vendor-credits/{uuid}/transition.
type vcTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

// Transition POST /api/tenant/vendor-credits/{uuid}/transition
//
// approvalTargetStatus/actionForTransition are declared once in
// creditmemo_transition.go and reused here unchanged: VCRD's DRFT->APPV
// approval target is the same literal "APPV" string, so the gating logic is
// identical (spec AD-2).
func (h *VendorCreditOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	// The body is decoded before the permission check because the target status
	// selects which permission applies. Decoding touches no database and leaks
	// nothing: a malformed body is a 400 either way.
	var req vcTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, id, actionForTransition(req.ToStatusCode))
	if !ok {
		return
	}
	before, _ := vendorcredit.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	vc, err := vendorcredit.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to transition vendor credit.")
		return
	}
	auditVC(r, pool, identityID, "transition", id, before, vc)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorCredit": vc})
}

// vcApplyRequest is the body of POST .../vendor-credits/{uuid}/apply.
type vcApplyRequest struct {
	VendorBillUUID string  `json:"vendorBillUuid"`
	Amount         float64 `json:"amount"`
}

// Apply applies part of a vendor credit's unapplied credit to a vendor bill.
// This mutates the target vendor bill's AP balance, so it requires
// vendor_bill:update scope on that specific bill in addition to
// vendor_credit:update + IDOR on the credit — a caller who can edit their
// own credit but can't see the target bill must not be able to move credit
// onto it.
func (h *VendorCreditOps) Apply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req vcApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VendorBillUUID == "" || req.Amount <= 0 {
		fail(w, http.StatusBadRequest, "vendorBillUuid and a positive amount are required.")
		return
	}
	if !h.vendorBillInScopeForUpdate(w, r, pool, identityID, req.VendorBillUUID) {
		return
	}
	before, _ := vendorcredit.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	vc, err := vendorcredit.Apply(r.Context(), pool, id, req.VendorBillUUID, req.Amount, empID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to apply vendor credit.")
		return
	}
	auditVC(r, pool, identityID, "apply", id, before, vc)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorCredit": vc})
}

// vcReverseRequest is the body of POST .../vendor-credits/{uuid}/reverse.
type vcReverseRequest struct {
	VendorBillUUID string `json:"vendorBillUuid"`
}

// Reverse reverses a live application between a vendor credit and a vendor
// bill, restoring both rollups. Named "Reverse" per spec AD-4 (the request's
// vocabulary), not "Unapply" as creditmemo calls it.
func (h *VendorCreditOps) Reverse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req vcReverseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VendorBillUUID == "" {
		fail(w, http.StatusBadRequest, "vendorBillUuid is required.")
		return
	}
	if !h.vendorBillInScopeForUpdate(w, r, pool, identityID, req.VendorBillUUID) {
		return
	}
	before, _ := vendorcredit.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	vc, err := vendorcredit.Reverse(r.Context(), pool, id, req.VendorBillUUID, empID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to reverse vendor credit.")
		return
	}
	auditVC(r, pool, identityID, "reverse", id, before, vc)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorCredit": vc})
}

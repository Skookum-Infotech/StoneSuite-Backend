package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorpayment"
)

// vendorPayTransitionRequest is the body of POST .../transition.
type vendorPayTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

// Transition handles POST /api/tenant/vendor-payments/{uuid}/transition.
func (h *VendorPaymentOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var req vendorPayTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vp, err := vendorpayment.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to transition vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "transition", id, nil, vp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorPayment": vp})
}

// Approve handles POST /api/tenant/vendor-payments/{uuid}/approve — one
// approver's sign-off (spec AD-6). No request body.
func (h *VendorPaymentOps) Approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vp, err := vendorpayment.Approve(r.Context(), pool, id, empID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to approve vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "approve", id, nil, vp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorPayment": vp})
}

// vendorPayApplyRequest is the body of POST .../apply.
type vendorPayApplyRequest struct {
	VendorBillUUID string  `json:"vendorBillUuid"`
	Amount         float64 `json:"amount"`
}

// Apply handles POST /api/tenant/vendor-payments/{uuid}/apply. This mutates
// the target vendor bill's balance, so it requires vendor_bill:update scope
// on that specific bill in addition to vendor_payment:update + IDOR on the
// payment (spec AD-10) — a caller who can edit their own payment but can't
// see the target bill must not be able to move money onto it.
func (h *VendorPaymentOps) Apply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req vendorPayApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VendorBillUUID == "" || req.Amount <= 0 {
		fail(w, http.StatusBadRequest, "vendorBillUuid and a positive amount are required.")
		return
	}
	if !h.vendorBillInScopeForUpdate(w, r, pool, identityID, req.VendorBillUUID) {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vp, err := vendorpayment.Apply(r.Context(), pool, id, req.VendorBillUUID, req.Amount, empID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to apply vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "apply", id, nil, vp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorPayment": vp})
}

// vendorPayUnapplyRequest is the body of POST .../unapply.
type vendorPayUnapplyRequest struct {
	VendorBillUUID string `json:"vendorBillUuid"`
}

// Unapply handles POST /api/tenant/vendor-payments/{uuid}/unapply.
func (h *VendorPaymentOps) Unapply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req vendorPayUnapplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VendorBillUUID == "" {
		fail(w, http.StatusBadRequest, "vendorBillUuid is required.")
		return
	}
	if !h.vendorBillInScopeForUpdate(w, r, pool, identityID, req.VendorBillUUID) {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vp, err := vendorpayment.Unapply(r.Context(), pool, id, req.VendorBillUUID, empID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to unapply vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "unapply", id, nil, vp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorPayment": vp})
}

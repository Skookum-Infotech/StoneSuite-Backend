// controllers/vendorbill_payments.go
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

// RecordPayment POST /api/tenant/vendor-bills/{uuid}/payment
// body {"amount":500.00,"methodId":3,"referenceNumber":"CHK-1042","memo":"","paidAt":"2026-08-10"}
// Records one settlement against the bill (AD-7); recomputes amount_paid/
// balance_due and re-derives status. RBAC: vendor_bill:update (a payment
// mutates the bill's own AP rollup).
func (h *VendorBillOps) RecordPayment(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorbill.RecordPaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := vendorbill.RecordPayment(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to record payment.")
		return
	}
	auditVB(r, pool, identityID, "record_payment", uuid, before, after)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": after})
}

// Payments GET /api/tenant/vendor-bills/{uuid}/payments
// AP reconciliation view of the bill's live settlement ledger. RBAC: vendor_bill:read.
func (h *VendorBillOps) Payments(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authVBByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	payments, err := vendorbill.ListPayments(r.Context(), pool, uuid)
	if err != nil {
		vbFail(w, err, "Failed to load payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": uuid, "payments": payments})
}

// RemovePayment DELETE /api/tenant/vendor-bills/{uuid}/payments/{paymentId}
// Soft-deletes one ledger entry (the "unapply") and recomputes the AP
// rollup. RBAC: vendor_bill:update.
func (h *VendorBillOps) RemovePayment(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	paymentID := r.PathValue("paymentId")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	after, err := vendorbill.RemovePayment(r.Context(), pool, uuid, paymentID, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to remove payment.")
		return
	}
	auditVB(r, pool, identityID, "unapply_payment", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": after})
}

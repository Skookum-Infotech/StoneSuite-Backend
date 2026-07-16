package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
)

type invTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

func (h *InvoiceOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var req invTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	inv, err := invoice.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		invoiceFail(w, err, "Failed to transition invoice.")
		return
	}
	auditInvoice(r, pool, empID, "transition", id, nil, inv)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": inv})
}

type recordPaymentRequest struct {
	Amount float64 `json:"amount"`
}

// RecordPayment is the legacy quick-pay endpoint (spec AD-5): it now delegates
// to payment.QuickPay, which creates a Payment + one payment_application
// under the hood, instead of writing invoice_amount_paid directly. Path,
// request, and response shape are unchanged for API compatibility.
func (h *InvoiceOps) RecordPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req recordPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount <= 0 {
		fail(w, http.StatusBadRequest, "amount is required and must be positive.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	inv, err := payment.QuickPay(r.Context(), pool, id, req.Amount, empID)
	if err != nil {
		invoiceFail(w, err, "Failed to record payment.")
		return
	}
	auditInvoice(r, pool, empID, "payment", id, nil, inv)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": inv})
}

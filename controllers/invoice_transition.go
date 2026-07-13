package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
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

func (h *InvoiceOps) RecordPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	// Using ActionUpdate for payment, or maybe ActionTransition, but we'll use ActionUpdate
	// since they are modifying the invoice's financial state, and payments can trigger transitions.
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
	inv, err := invoice.RecordPayment(r.Context(), pool, id, req.Amount, empID)
	if err != nil {
		invoiceFail(w, err, "Failed to record payment.")
		return
	}
	auditInvoice(r, pool, empID, "payment", id, nil, inv)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": inv})
}

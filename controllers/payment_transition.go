package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/payment"
)

type payTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

func (h *PaymentOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var req payTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Transition(auditCtx(r, ""), pool, id, req.ToStatusCode, empID)
	if err != nil {
		paymentFail(w, err, "Failed to transition payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

type payApplyRequest struct {
	InvoiceUUID string  `json:"invoiceUuid"`
	Amount      float64 `json:"amount"`
}

// Apply applies part of a payment's unapplied balance to an invoice. This
// mutates the target invoice's AR balance, so it requires invoice:update
// scope on that specific invoice in addition to payment:update + IDOR on the
// payment (spec §9) — a caller who can edit their own payment but can't see
// the target invoice must not be able to move money onto it.
func (h *PaymentOps) Apply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req payApplyRequest
	raw, ok := readJSONBody(w, r, &req)
	if !ok {
		return
	}
	if req.InvoiceUUID == "" || !(req.Amount > 0) {
		fail(w, http.StatusBadRequest, "invoiceUuid and a positive amount are required.")
		return
	}
	key, ok := idempotencyKeyFromRequest(w, r)
	if !ok {
		return
	}
	if !h.invoiceInScopeForUpdate(w, r, pool, identityID, req.InvoiceUUID) {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	var applyKey, applyHash string
	if key != "" {
		applyKey = callerIdempotencyKey(identityID, key)
		// The hash covers the payment id too: the body alone names only the
		// invoice and amount, so one key must not replay across payments.
		applyHash = idempotency.HashRequest([]byte(id + "\n" + string(raw)))
	}
	p, err := payment.ApplyIdempotent(auditCtx(r, ""), pool, id, req.InvoiceUUID, req.Amount, empID, applyKey, applyHash)
	if err != nil {
		paymentFail(w, err, "Failed to apply payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

type payUnapplyRequest struct {
	InvoiceUUID string `json:"invoiceUuid"`
}

func (h *PaymentOps) Unapply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req payUnapplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InvoiceUUID == "" {
		fail(w, http.StatusBadRequest, "invoiceUuid is required.")
		return
	}
	if !h.invoiceInScopeForUpdate(w, r, pool, identityID, req.InvoiceUUID) {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Unapply(auditCtx(r, ""), pool, id, req.InvoiceUUID, empID)
	if err != nil {
		paymentFail(w, err, "Failed to unapply payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

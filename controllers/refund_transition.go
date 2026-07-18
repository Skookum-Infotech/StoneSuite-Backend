package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/refund"
)

type refundTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

// Transition moves a refund between statuses. actionForTransition (shared
// with CreditMemoOps, controllers/creditmemo_transition.go) picks
// refund:approve for the PEND->APPV move and refund:transition otherwise —
// approving a refund is what authorizes it to draw down a payment or credit
// memo, so it is a separate capability (spec AD-4).
func (h *RefundOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	// The body is decoded before the permission check because the target status
	// selects which permission applies. Decoding touches no database and leaks
	// nothing: a malformed body is a 400 either way.
	var req refundTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	pool, identityID, _, ok := h.authRefundByUUID(w, r, id, actionForTransition(req.ToStatusCode))
	if !ok {
		return
	}
	before, _ := refund.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	rf, err := refund.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		refundFail(w, err, "Failed to transition refund.")
		return
	}
	auditRefund(r, pool, empID, "transition", id, before, rf)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "refund": rf})
}

type refundApplyRequest struct {
	PaymentUUID    string  `json:"paymentUuid,omitempty"`
	CreditMemoUUID string  `json:"creditMemoUuid,omitempty"`
	Amount         float64 `json:"amount"`
}

// Apply draws down part of a refund's unapplied balance from exactly one
// source — either a payment's overpayment or a credit memo's unapplied
// balance (spec AD-2). This mutates the source's refunded-total rollup, so it
// requires payment:update or credit_memo:update + IDOR on that specific
// source in addition to refund:update + IDOR on the refund — a caller who can
// edit their own refund but can't see the target source must not be able to
// draw money from it.
func (h *RefundOps) Apply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authRefundByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req refundApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount <= 0 ||
		(req.PaymentUUID == "") == (req.CreditMemoUUID == "") {
		fail(w, http.StatusBadRequest, "exactly one of paymentUuid or creditMemoUuid, and a positive amount, are required.")
		return
	}
	if req.PaymentUUID != "" {
		if !h.paymentInScopeForUpdate(w, r, pool, identityID, req.PaymentUUID) {
			return
		}
	} else {
		if !h.creditMemoInScopeForUpdate(w, r, pool, identityID, req.CreditMemoUUID) {
			return
		}
	}
	before, _ := refund.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	rf, err := refund.Apply(r.Context(), pool, id, req.PaymentUUID, req.CreditMemoUUID, req.Amount, empID)
	if err != nil {
		refundFail(w, err, "Failed to apply refund.")
		return
	}
	auditRefund(r, pool, empID, "apply", id, before, rf)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "refund": rf})
}

type refundUnapplyRequest struct {
	PaymentUUID    string `json:"paymentUuid,omitempty"`
	CreditMemoUUID string `json:"creditMemoUuid,omitempty"`
}

func (h *RefundOps) Unapply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authRefundByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req refundUnapplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.PaymentUUID == "") == (req.CreditMemoUUID == "") {
		fail(w, http.StatusBadRequest, "exactly one of paymentUuid or creditMemoUuid is required.")
		return
	}
	if req.PaymentUUID != "" {
		if !h.paymentInScopeForUpdate(w, r, pool, identityID, req.PaymentUUID) {
			return
		}
	} else {
		if !h.creditMemoInScopeForUpdate(w, r, pool, identityID, req.CreditMemoUUID) {
			return
		}
	}
	before, _ := refund.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	rf, err := refund.Unapply(r.Context(), pool, id, req.PaymentUUID, req.CreditMemoUUID, empID)
	if err != nil {
		refundFail(w, err, "Failed to unapply refund.")
		return
	}
	auditRefund(r, pool, empID, "unapply", id, before, rf)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "refund": rf})
}

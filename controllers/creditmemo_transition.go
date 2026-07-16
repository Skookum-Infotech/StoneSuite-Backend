package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
)

type cmTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

// approvalTargetStatus is the one status whose entry is gated on
// credit_memo:approve rather than credit_memo:transition. Approving a memo is
// what authorizes real credit against AR, so it is a separate capability from
// moving the record around: a role can be granted create/read/update without
// ever being able to approve its own drafts (spec AD-8).
const approvalTargetStatus = "APPV"

// actionForTransition picks the permission a status move requires.
func actionForTransition(toStatusCode string) authz.Action {
	if toStatusCode == approvalTargetStatus {
		return authz.ActionApprove
	}
	return authz.ActionTransition
}

func (h *CreditMemoOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	// The body is decoded before the permission check because the target status
	// selects which permission applies. Decoding touches no database and leaks
	// nothing: a malformed body is a 400 either way.
	var req cmTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	pool, identityID, _, ok := h.authCreditMemoByUUID(w, r, id, actionForTransition(req.ToStatusCode))
	if !ok {
		return
	}
	before, _ := creditmemo.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	cm, err := creditmemo.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		creditMemoFail(w, err, "Failed to transition credit memo.")
		return
	}
	auditCreditMemo(r, pool, empID, "transition", id, before, cm)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "creditMemo": cm})
}

type cmApplyRequest struct {
	InvoiceUUID string  `json:"invoiceUuid"`
	Amount      float64 `json:"amount"`
}

// Apply applies part of a credit memo's unapplied credit to an invoice. This
// mutates the target invoice's AR balance, so it requires invoice:update scope
// on that specific invoice in addition to credit_memo:update + IDOR on the memo
// — a caller who can edit their own memo but can't see the target invoice must
// not be able to move credit onto it.
func (h *CreditMemoOps) Apply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCreditMemoByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req cmApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InvoiceUUID == "" || req.Amount <= 0 {
		fail(w, http.StatusBadRequest, "invoiceUuid and a positive amount are required.")
		return
	}
	if !h.invoiceInScopeForUpdate(w, r, pool, identityID, req.InvoiceUUID) {
		return
	}
	before, _ := creditmemo.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	cm, err := creditmemo.Apply(r.Context(), pool, id, req.InvoiceUUID, req.Amount, empID)
	if err != nil {
		creditMemoFail(w, err, "Failed to apply credit memo.")
		return
	}
	auditCreditMemo(r, pool, empID, "apply", id, before, cm)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "creditMemo": cm})
}

type cmUnapplyRequest struct {
	InvoiceUUID string `json:"invoiceUuid"`
}

func (h *CreditMemoOps) Unapply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCreditMemoByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req cmUnapplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InvoiceUUID == "" {
		fail(w, http.StatusBadRequest, "invoiceUuid is required.")
		return
	}
	if !h.invoiceInScopeForUpdate(w, r, pool, identityID, req.InvoiceUUID) {
		return
	}
	before, _ := creditmemo.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	cm, err := creditmemo.Unapply(r.Context(), pool, id, req.InvoiceUUID, empID)
	if err != nil {
		creditMemoFail(w, err, "Failed to unapply credit memo.")
		return
	}
	auditCreditMemo(r, pool, empID, "unapply", id, before, cm)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "creditMemo": cm})
}

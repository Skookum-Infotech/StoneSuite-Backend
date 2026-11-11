package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/invoice"
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
	notifyCustomerStatus(r.Context(), h.cp, identityID, p.Customer.ID, custResPayment, p.StatusCode, custDisplayPayment, p.Number, p.Amount, id)
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
	h.notifyPaymentApplied(r, pool, identityID, p, req)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

// notifyPaymentApplied best-effort-emails the payment's customer that a payment
// was received, and the invoice's customer when the invoice became part-paid or paid.
func (h *PaymentOps) notifyPaymentApplied(r *http.Request, pool *pgxpool.Pool, identityID string, p *payment.Payment, req payApplyRequest) {
	ctx := r.Context()
	ev := customerReceivedCopy().event(customerEventReceived, custResPayment, custDisplayPayment, p.Number, p.ID, req.Amount, time.Now())
	notifyCustomerEvent(ctx, h.cp, identityID, p.Customer.ID, ev)
	inv, err := invoice.Get(ctx, pool, req.InvoiceUUID)
	if err != nil {
		slog.WarnContext(ctx, "customer notify: load invoice after payment apply failed", "invoiceId", req.InvoiceUUID, "error", err)
		return
	}
	if inv.StatusCode != invoiceStatusPart && inv.StatusCode != invoiceStatusPaid {
		return
	}
	notifyCustomerStatus(ctx, h.cp, identityID, inv.Customer.ID, custResInvoice, inv.StatusCode, custDisplayInvoice, inv.Number, inv.GrandTotal, req.InvoiceUUID)
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

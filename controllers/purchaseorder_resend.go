package controllers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/tenancy"
)

// ResendToVendor POST /api/tenant/purchase-orders/{uuid}/resend
// optional body {"to":[],"cc":[],"subject":"","message":""}
//
// Emails the purchase order PDF to the vendor again without touching its
// status. Only an order the vendor already holds (Sent, Partially Received,
// Received) may be resent, so this is a retry or a reminder, never a way to
// mail an unapproved order. Same grant as "Send to Vendor": purchase_order:
// transition, plus the ownership-scope check.
//
// The send is recorded either way; emailSent/emailError say whether the email
// itself went, exactly as on the transition response.
func (h *PurchaseOrderOps) ResendToVendor(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, po, ok := h.authPOByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	if h.docs == nil {
		fail(w, http.StatusInternalServerError, "Email sending is not available.")
		return
	}
	if !purchaseorder.CanResendToVendor(po.StatusCode) {
		fail(w, http.StatusConflict, "Only a purchase order that has already been sent to the vendor can be resent.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	var req sendDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	doc, meta, serr := h.docs.loadRecordDoc(r.Context(), pool, tenant, uuid, poWorkflowKey)
	if serr != nil {
		fail(w, serr.Status, serr.Msg)
		return
	}
	p, serr := prepareSend(tenant.ID, uuid, doc, meta, req)
	if serr != nil {
		fail(w, serr.Status, serr.Msg)
		return
	}
	sendID, outcome, serr := h.docs.deliver(r.Context(), pool, identityID, po.OwnerUserID, p)
	if serr != nil {
		slog.WarnContext(r.Context(), "purchase order resend to vendor: email failed",
			"record", uuid, "status", serr.Status, "error", serr.Msg)
		fail(w, serr.Status, serr.Msg)
		return
	}

	resp := map[string]any{"success": true, "sendId": sendID, "sentTo": p.to}
	applyEmailOutcome(r.Context(), resp, outcome, "purchase order resend to vendor: email not delivered",
		"record", uuid, "send_id", sendID)
	writeJSON(w, http.StatusOK, resp)
}

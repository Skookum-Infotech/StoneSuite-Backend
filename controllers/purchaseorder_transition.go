package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// poWorkflowKey is the workflow key the purchase_order document loader is
// registered under in the DocumentOps loader map.
const poWorkflowKey = "purchase_order"

// WithVendorSender wires the document pipeline used to email the vendor when an
// order is moved to SENT, and returns h. Without it a move to SENT changes
// status only.
func (h *PurchaseOrderOps) WithVendorSender(docs *DocumentOps) *PurchaseOrderOps {
	h.docs = docs
	return h
}

// Transition POST /api/tenant/purchase-orders/{uuid}/transition
// body {"toStatusCode":"...","force":false,"send":{"to":[],"cc":[],"subject":"","message":""}}
// Any purchase_order:transition holder may move an order to PAPV or SENT; a
// move to any other status requires a super admin (403 otherwise).
//
// A move to SENT is "Send to Vendor": it emails the PO PDF to the vendor
// (recipients default to the vendor's contact email; the optional "send"
// object overrides them) and only actually moves the status once that email
// is confirmed sent -- "Sent" is meant to mean the vendor really has it, so a
// recipient that doesn't resolve or a delivery that fails leaves the order
// exactly where it was, same as if nothing had been clicked.
//
// force skips that gate; a non-super-admin's force is silently ignored, so
// only the status-change dropdown (admin-only in the UI) can actually use it.
// With force, the move happens first (as this endpoint always used to) and
// the email is attempted best-effort after, purely for its own sake --
// nothing it does blocks or undoes the move. The response still carries
// emailSent and, on failure, emailError either way.
func (h *PurchaseOrderOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, po, ok := h.authPOByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string         `json:"toStatusCode"`
		Force        bool           `json:"force"`
		Send         sendDocRequest `json:"send"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}

	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		poFail(w, err, "Failed to apply transition.")
		return
	}
	// purchase_order:transition lets anyone submit for approval or send to the
	// vendor; every other manual move is a super-admin override. Checked before
	// the store call so a direct API request cannot reach a move the UI hides.
	if !purchaseorder.NonAdminMayTransitionTo(req.ToStatusCode) && !isSuperAdmin {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourcePurchaseOrder),
			"action", string(authz.ActionTransition), "record", uuid, "to_status", req.ToStatusCode)
		fail(w, http.StatusForbidden, "Only an administrator can move a purchase order to that status.")
		return
	}
	force := req.Force && isSuperAdmin

	emailVendor := h.docs != nil && req.ToStatusCode == purchaseorder.SentStatusCode
	var tenant *tenancy.Tenant
	if emailVendor {
		var terr error
		if tenant, terr = tenancy.TenantFromContext(r.Context()); terr != nil {
			fail(w, http.StatusInternalServerError, "Tenant not resolved.")
			return
		}
		if serr := h.docs.preflightRecordSend(r.Context(), pool, tenant, uuid, poWorkflowKey, req.Send); serr != nil {
			fail(w, serr.Status, serr.Msg+" The purchase order was not sent.")
			return
		}
	}

	resp := map[string]any{"success": true}

	// The normal Send to Vendor move happens only once the email itself is
	// confirmed sent -- so, unlike force below, the email goes out BEFORE the
	// status change. The PDF it carries is still labeled Sent (sendRecord's
	// statusOverride), even though that hasn't landed in the database yet.
	if emailVendor && !force {
		sendID, outcome, serr := h.docs.sendRecord(r.Context(), pool, tenant, identityID, po.OwnerUserID, uuid, poWorkflowKey, req.Send, purchaseorder.SentStatusLabel)
		if serr != nil {
			slog.WarnContext(r.Context(), "purchase order send to vendor: email failed",
				"record", uuid, "status", serr.Status, "error", serr.Msg)
			fail(w, serr.Status, serr.Msg+" The purchase order was not sent.")
			return
		}
		if outcome.Status == services.EmailStatusFailed {
			slog.WarnContext(r.Context(), "purchase order send to vendor: email not delivered",
				"record", uuid, "email_status", string(outcome.Status), "detail", outcome.Detail)
			fail(w, http.StatusBadGateway, outcome.UserMessage()+" The purchase order was not sent.")
			return
		}
		resp["sendId"] = sendID
		applyEmailOutcome(r.Context(), resp, outcome, "purchase order send to vendor: email not confirmed delivered",
			"record", uuid, "send_id", sendID)
	}

	updated, err := purchaseorder.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		poFail(w, err, "Failed to apply transition.")
		return
	}
	auditPO(r, pool, identityID, "transition", uuid, nil, updated)
	resp["purchaseOrder"] = updated

	// force's email, unlike the normal path above, is attempted only after the
	// move has landed -- the same best-effort order this endpoint always used,
	// kept for the admin override: nothing here blocks or undoes the move.
	// Re-check the landed status: only an order that actually reached SENT is
	// mailed (a gate may have parked it elsewhere).
	if emailVendor && force && updated.StatusCode == purchaseorder.SentStatusCode {
		sendID, outcome, serr := h.docs.sendRecord(r.Context(), pool, tenant, identityID, po.OwnerUserID, uuid, poWorkflowKey, req.Send, "")
		if serr != nil {
			slog.WarnContext(r.Context(), "purchase order send to vendor: email failed",
				"record", uuid, "status", serr.Status, "error", serr.Msg)
			resp["emailSent"] = false
			resp["emailError"] = serr.Msg
		} else {
			resp["sendId"] = sendID
			applyEmailOutcome(r.Context(), resp, outcome, "purchase order send to vendor: email not delivered",
				"record", uuid, "send_id", sendID)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

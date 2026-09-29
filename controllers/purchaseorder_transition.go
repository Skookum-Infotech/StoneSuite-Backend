package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
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
// body {"toStatusCode":"...","send":{"to":[],"cc":[],"subject":"","message":""}}
// Any purchase_order:transition holder may move an order to PAPV or SENT; a
// move to any other status requires a super admin (403 otherwise).
//
// A move to SENT is "Send to Vendor": it changes the status AND emails the PO
// PDF to the vendor (recipients default to the vendor's contact email; the
// optional "send" object overrides them). The recipient is validated before the
// status changes, so an order with no reachable vendor email is refused rather
// than marked sent. Delivery after the status change is best-effort: the
// response carries emailSent and, on failure, emailError.
func (h *PurchaseOrderOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, po, ok := h.authPOByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string         `json:"toStatusCode"`
		Send         sendDocRequest `json:"send"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	// purchase_order:transition lets anyone submit for approval or send to the
	// vendor; every other manual move is a super-admin override. Checked before
	// the store call so a direct API request cannot reach a move the UI hides.
	if !purchaseorder.NonAdminMayTransitionTo(req.ToStatusCode) {
		isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
		if err != nil {
			poFail(w, err, "Failed to apply transition.")
			return
		}
		if !isSuperAdmin {
			logSecurityEvent(r, "permission_denied",
				"identity", identityID, "resource", string(authz.ResourcePurchaseOrder),
				"action", string(authz.ActionTransition), "record", uuid, "to_status", req.ToStatusCode)
			fail(w, http.StatusForbidden, "Only an administrator can move a purchase order to that status.")
			return
		}
	}

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

	updated, err := purchaseorder.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		poFail(w, err, "Failed to apply transition.")
		return
	}
	auditPO(r, pool, identityID, "transition", uuid, nil, updated)

	resp := map[string]any{"success": true, "purchaseOrder": updated}
	// Re-check the landed status: only an order that actually reached SENT is
	// mailed (a gate may have parked it elsewhere).
	if emailVendor && updated.StatusCode == purchaseorder.SentStatusCode {
		// The PDF is rendered after the move so it shows the Sent status.
		sendID, serr := h.docs.sendRecord(r.Context(), pool, tenant, identityID, po.OwnerUserID, uuid, poWorkflowKey, req.Send)
		resp["emailSent"] = serr == nil
		if serr != nil {
			slog.WarnContext(r.Context(), "purchase order send to vendor: email failed",
				"record", uuid, "status", serr.Status, "error", serr.Msg)
			resp["emailError"] = serr.Msg
		} else {
			resp["sendId"] = sendID
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

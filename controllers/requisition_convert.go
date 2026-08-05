// controllers/requisition_convert.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
)

// Convert POST /api/tenant/requisitions/{uuid}/convert  body {"vendorUuid":"..."}
//
// Creates a Purchase Order as a snapshot copy of the live, approved
// requisition (AD-8 of the Requisition design). Requires requisition:read on
// the source (IDOR-guarded, mirrors every other single-record requisition
// action) and purchase_order:create on the target — a caller who can view a
// requisition but cannot create purchase orders must not be able to spawn
// one via convert. vendorUuid is required: a requisition's own suggested
// vendor is only ever a suggestion, never silently promoted to the PO's
// mandatory vendor. Idempotent: replaying the call on an already-converted
// requisition returns the existing purchase order with 200 instead of
// creating a duplicate. Mirrors controllers/salesorder_convert.go's Convert.
func (h *RequisitionOps) Convert(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authReqnByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}

	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourcePurchaseOrder, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourcePurchaseOrder), "action", string(authz.ActionCreate),
			"context", "requisition_convert", "source_record", uuid)
		fail(w, http.StatusForbidden, "You do not have permission to create purchase orders.")
		return
	}

	var req convertRequisitionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}

	empID := resolveEmployeeID(r, identityID)
	po, created, err := purchaseorder.ConvertFromRequisition(r.Context(), pool, uuid, req.VendorUUID, empID)
	if err != nil {
		switch {
		case errors.Is(err, purchaseorder.ErrRequisitionNotFound):
			fail(w, http.StatusNotFound, "Requisition not found.")
		default:
			var ce purchaseorder.ClientError
			if errors.As(err, &ce) {
				fail(w, http.StatusBadRequest, ce.Error())
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to convert requisition to purchase order.")
		}
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		auditPO(r, pool, identityID, "convert", po.ID, nil, po)
	}
	writeJSON(w, status, map[string]any{"success": true, "purchaseOrder": po, "created": created})
}

// convertRequisitionRequest is the Convert endpoint's request body.
type convertRequisitionRequest struct {
	VendorUUID string `json:"vendorUuid"`
}

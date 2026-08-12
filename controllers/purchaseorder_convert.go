// controllers/purchaseorder_convert.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

// ConvertToBill POST /api/tenant/purchase-orders/{uuid}/convert-to-bill
//
// Creates a Vendor Bill as a snapshot copy of the live, received purchase
// order (AD-8 of the Vendor Bill design). Requires purchase_order:read on
// the source (IDOR-guarded, mirrors every other single-record purchase
// order action) and vendor_bill:create on the target -- a caller who can
// view a purchase order but cannot create vendor bills must not be able to
// spawn one via convert. Unlike RequisitionOps.Convert, this is NOT
// idempotent: a purchase order may be billed more than once (vendors
// routinely invoice in installments), so every call creates a new bill.
// Mirrors controllers/requisition_convert.go's Convert.
func (h *PurchaseOrderOps) ConvertToBill(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}

	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceVendorBill, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceVendorBill), "action", string(authz.ActionCreate),
			"context", "purchase_order_convert_to_bill", "source_record", uuid)
		fail(w, http.StatusForbidden, "You do not have permission to create vendor bills.")
		return
	}

	bill, err := vendorbill.ConvertFromPurchaseOrder(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		switch {
		case errors.Is(err, vendorbill.ErrPurchaseOrderNotFound):
			fail(w, http.StatusNotFound, "Purchase order not found.")
		default:
			if vendorbill.IsClientError(err) {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to convert purchase order to vendor bill.")
		}
		return
	}

	auditVB(r, pool, identityID, "convert", bill.ID, nil, bill)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": bill, "created": true})
}

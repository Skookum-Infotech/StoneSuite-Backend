// controllers/salesorder_convert.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
)

// Convert POST /api/tenant/sales-orders/{uuid}/convert
//
// Creates an Invoice as a full snapshot copy of the live sales order (spec
// AD-6). Requires sales_order:read on the source (IDOR-guarded, mirrors
// every other single-record sales order action) and invoice:create on the
// target — a caller who can view a sales order but cannot create invoices
// must not be able to spawn one via convert. Idempotent: replaying the call
// on an already-converted sales order returns the existing invoice with 200
// instead of creating a duplicate.
func (h *SalesOrderOps) Convert(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}

	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInvoice, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInvoice), "action", string(authz.ActionCreate),
			"context", "sales_order_convert", "source_record", uuid)
		fail(w, http.StatusForbidden, "You do not have permission to create invoices.")
		return
	}

	empID := resolveEmployeeID(r, identityID)
	inv, created, err := invoice.ConvertFromSalesOrder(r.Context(), pool, uuid, empID)
	if err != nil {
		switch {
		case errors.Is(err, invoice.ErrSalesOrderNotFound):
			fail(w, http.StatusNotFound, "Sales order not found.")
		default:
			var ce invoice.ClientError
			if errors.As(err, &ce) {
				fail(w, http.StatusBadRequest, ce.Error())
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to convert sales order to invoice.")
		}
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		auditInvoice(r, pool, empID, "convert", inv.ID, nil, inv)
	}
	writeJSON(w, status, map[string]any{"success": true, "invoice": inv, "created": created})
}

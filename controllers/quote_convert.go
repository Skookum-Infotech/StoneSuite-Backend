// controllers/quote_convert.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
)

// Convert POST /api/tenant/quotes/{uuid}/convert
//
// Creates a Sales Order as a full snapshot copy of the live quote (spec
// AD-6). Requires quote:read on the source (IDOR-guarded, mirrors every
// other single-record quote action) and sales_order:create on the target —
// a caller who can view a quote but cannot create sales orders must not be
// able to spawn one via convert. Idempotent: replaying the call on an
// already-converted quote returns the existing sales order with 200 instead
// of creating a duplicate.
func (h *QuoteOps) Convert(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}

	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceSalesOrder, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceSalesOrder), "action", string(authz.ActionCreate),
			"context", "quote_convert", "source_record", uuid)
		fail(w, http.StatusForbidden, "You do not have permission to create sales orders.")
		return
	}

	empID := resolveEmployeeID(r, identityID)
	order, created, err := salesorder.ConvertFromQuote(r.Context(), pool, uuid, empID)
	if err != nil {
		switch {
		case errors.Is(err, salesorder.ErrQuoteNotFound):
			fail(w, http.StatusNotFound, "Quote not found.")
		case salesorder.IsClientError(err):
			fail(w, http.StatusBadRequest, err.Error())
		default:
			fail(w, http.StatusInternalServerError, "Failed to convert quote to sales order.")
		}
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		auditSO(r, pool, identityID, "convert", order.ID, nil, order)
	}
	writeJSON(w, status, map[string]any{"success": true, "salesOrder": order, "created": created})
}

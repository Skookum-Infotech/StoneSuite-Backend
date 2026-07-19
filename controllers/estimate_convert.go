// controllers/estimate_convert.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/quote"
)

// Convert POST /api/tenant/estimates/{uuid}/convert
//
// Creates a Quote as a full snapshot copy of the live estimate (spec AD-6).
// Requires estimate:read on the source (IDOR-guarded, mirrors every other
// single-record estimate action) and quote:create on the target — a caller
// who can view an estimate but cannot create quotes must not be able to spawn
// one via convert. Idempotent: replaying the call on an already-converted
// estimate returns the existing quote with 200 instead of creating a
// duplicate.
func (h *EstimateOps) Convert(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}

	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceQuote, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceQuote), "action", string(authz.ActionCreate),
			"context", "estimate_convert", "source_record", uuid)
		fail(w, http.StatusForbidden, "You do not have permission to create quotes.")
		return
	}

	empID := resolveEmployeeID(r, identityID)
	q, created, err := quote.ConvertFromEstimate(r.Context(), pool, uuid, empID)
	if err != nil {
		switch {
		case errors.Is(err, quote.ErrEstimateNotFound):
			fail(w, http.StatusNotFound, "Estimate not found.")
		case quote.IsClientError(err):
			fail(w, http.StatusBadRequest, err.Error())
		default:
			fail(w, http.StatusInternalServerError, "Failed to convert estimate to quote.")
		}
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		auditQuote(r, pool, identityID, "convert", q.ID, nil, q)
	}
	writeJSON(w, status, map[string]any{"success": true, "quote": q, "created": created})
}

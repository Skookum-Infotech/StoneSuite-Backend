package controllers

// inventory_counts_auth.go — the cycle-count controller's security chain,
// split from the handlers to keep both files under the size cap.

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventorycount"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

func (h *InventoryCountOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryCount, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryCount), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory counts.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record mutations, 404ing so
// document ids cannot be enumerated.
func (h *InventoryCountOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventorycount.Get(r.Context(), pool, r.PathValue("uuid")); err != nil {
		countFail(w, err, "Failed to load count.")
		return nil, "", false
	}
	return pool, identityID, true
}

func countFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	switch {
	case errors.Is(err, inventorycount.ErrNotFound):
		fail(w, http.StatusNotFound, "Count not found.")
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, inventorycount.ErrNotEditable):
		fail(w, http.StatusConflict, "This count can no longer be edited.")
	case inventorycount.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, fallback)
	}
}

// mayApprove checks the separate approve grant, reporting the denial itself.
//
// Signing off a count's variances is a write-off of real stock, so it is kept
// distinct from plain transition rights: the crew that counted should not be
// the authority that accepts what they found.
func (h *InventoryCountOps) mayApprove(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInventoryCount, authz.ActionApprove)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInventoryCount), "action", string(authz.ActionApprove))
		fail(w, http.StatusForbidden, "You do not have permission to approve inventory counts.")
		return false
	}
	return true
}

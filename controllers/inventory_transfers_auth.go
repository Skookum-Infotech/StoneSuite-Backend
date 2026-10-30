package controllers

// inventory_transfers_auth.go — the transfer controller's security chain,
// split out to keep inventory_transfers.go under the file-size cap.

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventorytransfer"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

func (h *InventoryTransferOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryTransfer, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryTransfer), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory transfers.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record mutations, 404ing so
// document ids cannot be enumerated.
func (h *InventoryTransferOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventorytransfer.Get(r.Context(), pool, r.PathValue("uuid")); err != nil {
		transferFail(w, err, "Failed to load transfer.")
		return nil, "", false
	}
	return pool, identityID, true
}

// mayApprove checks the separate approve grant, reporting the denial itself.
func (h *InventoryTransferOps) mayApprove(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInventoryTransfer, authz.ActionApprove)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInventoryTransfer), "action", string(authz.ActionApprove))
		fail(w, http.StatusForbidden, "You do not have permission to approve inventory transfers.")
		return false
	}
	return true
}

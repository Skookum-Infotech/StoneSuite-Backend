// controllers/vendorbill_audit.go
package controllers

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// vbAuditCtx resolves the acting user for a vendor bill write and returns the
// request context the store needs to write its audit row in-transaction.
func vbAuditCtx(r *http.Request, pool *pgxpool.Pool, identityID string) context.Context {
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	return auditCtx(r, actorUserID)
}

// Audit GET /api/tenant/vendor-bills/{uuid}/audit
// Returns the unified audit trail for a single vendor bill (most recent first).
func (h *VendorBillOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVBByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := loadAuditEntries(r.Context(), pool, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "audit": entries,
	})
}

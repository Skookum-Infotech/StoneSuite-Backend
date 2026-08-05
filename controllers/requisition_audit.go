// controllers/requisition_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/requisition"
	"stonesuite-backend/workflow"
)

// reqnSnapshot flattens a requisition into a JSON-able map for the audit
// trail, mirroring poSnapshot for the Requisition shape.
func reqnSnapshot(r *requisition.Requisition) map[string]any {
	if r == nil {
		return nil
	}
	snap := map[string]any{
		"id":                r.ID,
		"requisitionNumber": r.Number,
		"status":            r.Status,
		"estimatedTotal":    r.EstimatedTotal,
	}
	if r.Vendor != nil {
		snap["vendorId"] = r.Vendor.ID
	}
	return snap
}

// auditReqn records a Requisition mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditPO.
func auditReqn(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldReqn, newReqn *requisition.Requisition) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "requisition", recordID, "requisition",
		reqnSnapshot(oldReqn), reqnSnapshot(newReqn), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("requisition: audit %s %s: %v", action, recordID, err)
	}
}

// auditReqnDelete is the delete-specific variant, mirroring auditPODelete.
func auditReqnDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldReqn *requisition.Requisition) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "requisition", recordID, "requisition",
		reqnSnapshot(oldReqn), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("requisition: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/requisitions/{uuid}/audit
// Returns the unified audit trail for a single requisition (most recent first).
func (h *RequisitionOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authReqnByUUID(w, r, id, authz.ActionRead)
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

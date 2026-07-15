// controllers/estimate_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/estimate"
	"stonesuite-backend/workflow"
)

// estimateSnapshot flattens an estimate into a JSON-able map for the audit
// trail, mirroring soSnapshot (salesorder_audit.go) for the Estimate shape.
func estimateSnapshot(e *estimate.Estimate) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"id":             e.ID,
		"estimateNumber": e.Number,
		"status":         e.Status,
		"customerId":     e.Customer.ID,
		"grandTotal":     e.GrandTotal,
	}
}

// auditEstimate records an Estimate mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditSO.
func auditEstimate(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldEstimate, newEstimate *estimate.Estimate) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "estimate", recordID, "estimate",
		estimateSnapshot(oldEstimate), estimateSnapshot(newEstimate), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("estimate: audit %s %s: %v", action, recordID, err)
	}
}

// auditEstimateDelete is the delete-specific variant, mirroring auditSODelete.
func auditEstimateDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldEstimate *estimate.Estimate) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "estimate", recordID, "estimate",
		estimateSnapshot(oldEstimate), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("estimate: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/estimates/{uuid}/audit
// Returns the unified audit trail for a single estimate (most recent first).
func (h *EstimateOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authEstimateByUUID(w, r, id, authz.ActionRead)
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

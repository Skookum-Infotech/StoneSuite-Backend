package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/workflow"
)

// soSnapshot flattens an order into a JSON-able map for the audit trail,
// mirroring recordSnapshot (crm_audit.go) for the Sales Order shape.
func soSnapshot(o *salesorder.Order) map[string]any {
	if o == nil {
		return nil
	}
	return map[string]any{
		"id":               o.ID,
		"salesOrderNumber": o.Number,
		"status":           o.Status,
		"customerId":       o.Customer.ID,
		"grandTotal":       o.GrandTotal,
	}
}

// auditSO records a Sales Order mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditCRM.
func auditSO(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldOrder, newOrder *salesorder.Order) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "sales_order", recordID, "sales_order",
		soSnapshot(oldOrder), soSnapshot(newOrder), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("salesorder: audit %s %s: %v", action, recordID, err)
	}
}

// auditSODelete is the delete-specific variant, mirroring auditCRMDelete.
func auditSODelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldOrder *salesorder.Order) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "sales_order", recordID, "sales_order",
		soSnapshot(oldOrder), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("salesorder: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/sales-orders/{uuid}/audit
// Returns the unified audit trail for a single sales order (most recent first).
func (h *SalesOrderOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authSOByUUID(w, r, id, authz.ActionRead)
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

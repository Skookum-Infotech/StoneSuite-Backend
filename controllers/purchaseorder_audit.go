// controllers/purchaseorder_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/workflow"
)

// poSnapshot flattens a purchase order into a JSON-able map for the audit
// trail, mirroring estimateSnapshot for the Purchase Order shape.
func poSnapshot(p *purchaseorder.PurchaseOrder) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":                  p.ID,
		"purchaseOrderNumber": p.Number,
		"status":              p.Status,
		"vendorId":            p.Vendor.ID,
		"grandTotal":          p.GrandTotal,
	}
}

// auditPO records a Purchase Order mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditEstimate.
func auditPO(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldPO, newPO *purchaseorder.PurchaseOrder) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "purchase_order", recordID, "purchase_order",
		poSnapshot(oldPO), poSnapshot(newPO), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("purchaseorder: audit %s %s: %v", action, recordID, err)
	}
}

// auditPODelete is the delete-specific variant, mirroring auditEstimateDelete.
func auditPODelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldPO *purchaseorder.PurchaseOrder) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "purchase_order", recordID, "purchase_order",
		poSnapshot(oldPO), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("purchaseorder: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/purchase-orders/{uuid}/audit
// Returns the unified audit trail for a single purchase order (most recent first).
func (h *PurchaseOrderOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authPOByUUID(w, r, id, authz.ActionRead)
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

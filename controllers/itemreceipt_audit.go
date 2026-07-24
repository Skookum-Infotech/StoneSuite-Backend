// controllers/itemreceipt_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/workflow"
)

// irSnapshot flattens an item receipt into a JSON-able map for the audit
// trail, mirroring poSnapshot for the Item Receipt shape.
func irSnapshot(r *itemreceipt.ItemReceipt) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"id":                r.ID,
		"itemReceiptNumber": r.Number,
		"status":            r.Status,
		"purchaseOrderId":   r.PurchaseOrder.ID,
		"vendorId":          r.Vendor.ID,
		"warehouseId":       r.WarehouseID,
	}
}

// auditIR records an Item Receipt mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditPO.
func auditIR(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldIR, newIR *itemreceipt.ItemReceipt) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "item_receipt", recordID, "item_receipt",
		irSnapshot(oldIR), irSnapshot(newIR), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("itemreceipt: audit %s %s: %v", action, recordID, err)
	}
}

// auditIRDelete is the delete-specific variant, mirroring auditPODelete.
func auditIRDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldIR *itemreceipt.ItemReceipt) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "item_receipt", recordID, "item_receipt",
		irSnapshot(oldIR), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("itemreceipt: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/item-receipts/{uuid}/audit
// Returns the unified audit trail for a single item receipt (most recent first).
func (h *ItemReceiptOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authIRByUUID(w, r, id, authz.ActionRead)
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

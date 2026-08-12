// controllers/vendorbill_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/workflow"
)

// vbSnapshot flattens a vendor bill into a JSON-able map for the audit
// trail, mirroring poSnapshot for the Vendor Bill shape.
func vbSnapshot(b *vendorbill.VendorBill) map[string]any {
	if b == nil {
		return nil
	}
	return map[string]any{
		"id":               b.ID,
		"vendorBillNumber": b.Number,
		"status":           b.StatusName,
		"vendorId":         b.Vendor.ID,
		"grandTotal":       b.GrandTotal,
		"balanceDue":       b.BalanceDue,
	}
}

// auditVB records a Vendor Bill mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditPO.
func auditVB(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldBill, newBill *vendorbill.VendorBill) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "vendor_bill", recordID, "vendor_bill",
		vbSnapshot(oldBill), vbSnapshot(newBill), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorbill: audit %s %s: %v", action, recordID, err)
	}
}

// auditVBDelete is the delete-specific variant, mirroring auditPODelete.
func auditVBDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldBill *vendorbill.VendorBill) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "vendor_bill", recordID, "vendor_bill",
		vbSnapshot(oldBill), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorbill: audit delete %s: %v", recordID, err)
	}
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

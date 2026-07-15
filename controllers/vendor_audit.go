package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendors"
	"stonesuite-backend/workflow"
)

// vendorSnapshot flattens a vendor into a JSON-able map for the audit trail,
// mirroring soSnapshot (salesorder_audit.go).
func vendorSnapshot(v *vendors.Vendor) map[string]any {
	if v == nil {
		return nil
	}
	return map[string]any{
		"id":           v.ID,
		"vendorNumber": v.Number,
		"status":       v.Status,
		"vendorType":   v.VendorType,
		"displayName":  v.DisplayName,
	}
}

// auditVendor records a Vendor mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditSO.
func auditVendor(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldVendor, newVendor *vendors.Vendor) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "vendor", recordID, "vendor",
		vendorSnapshot(oldVendor), vendorSnapshot(newVendor), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendors: audit %s %s: %v", action, recordID, err)
	}
}

// auditVendorDelete is the delete-specific variant, mirroring auditSODelete.
func auditVendorDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldVendor *vendors.Vendor) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "vendor", recordID, "vendor",
		vendorSnapshot(oldVendor), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendors: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/vendors/{uuid}/audit
// Returns the unified audit trail for a single vendor (most recent first).
func (h *VendorOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVendorByUUID(w, r, id, authz.ActionRead)
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

// controllers/vendorcredit_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/workflow"
)

// vcSnapshot flattens a vendor credit into a JSON-able map for the audit
// trail, mirroring vbSnapshot for the Vendor Credit shape.
func vcSnapshot(c *vendorcredit.VendorCredit) map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"id":                 c.ID,
		"vendorCreditNumber": c.Number,
		"status":             c.StatusName,
		"vendorId":           c.Vendor.ID,
		"grandTotal":         c.GrandTotal,
		"appliedTotal":       c.AppliedTotal,
		"unappliedAmount":    c.UnappliedAmount,
	}
}

// auditVC records a Vendor Credit mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditVB.
func auditVC(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldCredit, newCredit *vendorcredit.VendorCredit) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "vendor_credit", recordID, "vendor_credit",
		vcSnapshot(oldCredit), vcSnapshot(newCredit), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorcredit: audit %s %s: %v", action, recordID, err)
	}
}

// auditVCDelete is the delete-specific variant, mirroring auditVBDelete.
func auditVCDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldCredit *vendorcredit.VendorCredit) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "vendor_credit", recordID, "vendor_credit",
		vcSnapshot(oldCredit), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorcredit: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/vendor-credits/{uuid}/audit
// Returns the unified audit trail for a single vendor credit (most recent first).
func (h *VendorCreditOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVendorCreditByUUID(w, r, id, authz.ActionRead)
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

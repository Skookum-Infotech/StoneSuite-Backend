package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/workflow"
)

// vendorBillSnapshot flattens a VendorBill into the map recorded in audit_logs.
func vendorBillSnapshot(vb *vendorbill.VendorBill) map[string]any {
	if vb == nil {
		return nil
	}
	return map[string]any{
		"id":           vb.ID,
		"number":       vb.Number,
		"statusCode":   vb.StatusCode,
		"vendorId":     vb.Vendor.ID,
		"ownerUserId":  vb.OwnerUserID,
		"grandTotal":   vb.GrandTotal,
		"amountPaid":   vb.AmountPaid,
		"balanceDue":   vb.BalanceDue,
		"customFields": vb.CustomFields,
	}
}

// auditVendorBill records a create/update/delete/transition event for a vendor bill.
func auditVendorBill(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, vendorBillID string, oldBill, newBill *vendorbill.VendorBill) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceVendorBill), vendorBillID, "vendor_bill",
		vendorBillSnapshot(oldBill), vendorBillSnapshot(newBill), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorbill: audit %s %s: %v", action, vendorBillID, err)
	}
}

// vendorBillAuditEntry is a single row of a vendor bill's audit trail.
type vendorBillAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

// Audit returns the audit trail for a single vendor bill (GET /api/tenant/vendor-bills/{uuid}/audit).
func (h *VendorBillOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVendorBillByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT al.action,
		       COALESCE(u.full_name, u.email, ''),
		       COALESCE(host(al.ip_address),''), COALESCE(al.app_version,''),
		       al.old_value, al.new_value, al.created_at
		FROM audit_logs al
		LEFT JOIN users u ON u.id = al.actor_user_id
		WHERE al.resource_id = $1 AND al.resource = $2
		ORDER BY al.created_at DESC
		LIMIT 200`, id, string(authz.ResourceVendorBill))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []vendorBillAuditEntry{}
	for rows.Next() {
		var (
			e              vendorBillAuditEntry
			oldRaw, newRaw []byte
		)
		if err := rows.Scan(&e.Action, &e.ActorName,
			&e.IPAddress, &e.AppVersion, &oldRaw, &newRaw, &e.At); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read audit trail.")
			return
		}
		if len(oldRaw) > 0 {
			_ = json.Unmarshal(oldRaw, &e.OldValue)
		}
		if len(newRaw) > 0 {
			_ = json.Unmarshal(newRaw, &e.NewValue)
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "audit": entries,
	})
}

package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorpayment"
	"stonesuite-backend/workflow"
)

// vendorPaymentSnapshot flattens a VendorPayment into the map recorded in audit_logs.
func vendorPaymentSnapshot(p *vendorpayment.VendorPayment) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":              p.ID,
		"number":          p.Number,
		"statusCode":      p.StatusCode,
		"vendorId":        p.Vendor.ID,
		"ownerUserId":     p.OwnerUserID,
		"amount":          p.Amount,
		"appliedTotal":    p.AppliedTotal,
		"unappliedAmount": p.UnappliedAmount,
		"customFields":    p.CustomFields,
	}
}

// auditVendorPayment records a create/update/delete/transition event for a vendor payment.
func auditVendorPayment(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, paymentID string, oldPayment, newPayment *vendorpayment.VendorPayment) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceVendorPayment), paymentID, "vendor_payment",
		vendorPaymentSnapshot(oldPayment), vendorPaymentSnapshot(newPayment), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorpayment: audit %s %s: %v", action, paymentID, err)
	}
}

// vendorPayAuditEntry is a single row of a vendor payment's audit trail.
type vendorPayAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

// Audit returns the audit trail for a single vendor payment (GET /api/tenant/vendor-payments/{uuid}/audit).
func (h *VendorPaymentOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourceVendorPayment))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []vendorPayAuditEntry{}
	for rows.Next() {
		var (
			e              vendorPayAuditEntry
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

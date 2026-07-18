package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/refund"
	"stonesuite-backend/workflow"
)

// refundSnapshot flattens a Refund into the map recorded in audit_logs.
func refundSnapshot(rf *refund.Refund) map[string]any {
	if rf == nil {
		return nil
	}
	return map[string]any{
		"id":              rf.ID,
		"number":          rf.Number,
		"statusCode":      rf.StatusCode,
		"customerId":      rf.Customer.ID,
		"ownerUserId":     rf.OwnerUserID,
		"paymentId":       rf.PaymentID,
		"creditMemoId":    rf.CreditMemoID,
		"amount":          rf.Amount,
		"appliedTotal":    rf.AppliedTotal,
		"unappliedAmount": rf.UnappliedAmount,
		"customFields":    rf.CustomFields,
	}
}

// auditRefund records a create/update/delete/transition/apply/unapply event for a refund.
func auditRefund(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, refundID string, oldRefund, newRefund *refund.Refund) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceRefund), refundID, "refund",
		refundSnapshot(oldRefund), refundSnapshot(newRefund), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("refund: audit %s %s: %v", action, refundID, err)
	}
}

// refundAuditEntry is a single row of a refund's audit trail.
type refundAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

// Audit returns the audit trail for a single refund (GET /api/tenant/refunds/{uuid}/audit).
func (h *RefundOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authRefundByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourceRefund))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []refundAuditEntry{}
	for rows.Next() {
		var (
			e              refundAuditEntry
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

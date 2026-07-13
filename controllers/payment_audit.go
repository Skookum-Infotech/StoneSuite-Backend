package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/payment"
	"stonesuite-backend/workflow"
)

// paymentSnapshot flattens a Payment into the map recorded in audit_logs.
func paymentSnapshot(p *payment.Payment) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":              p.ID,
		"number":          p.Number,
		"statusCode":      p.StatusCode,
		"customerId":      p.Customer.ID,
		"ownerUserId":     p.OwnerUserID,
		"amount":          p.Amount,
		"appliedTotal":    p.AppliedTotal,
		"unappliedAmount": p.UnappliedAmount,
		"customFields":    p.CustomFields,
	}
}

// auditPayment records a create/update/delete/transition event for a payment.
func auditPayment(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, paymentID string, oldPayment, newPayment *payment.Payment) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourcePayment), paymentID, "payment",
		paymentSnapshot(oldPayment), paymentSnapshot(newPayment), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("payment: audit %s %s: %v", action, paymentID, err)
	}
}

// payAuditEntry is a single row of a payment's audit trail.
type payAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

// Audit returns the audit trail for a single payment (GET /api/tenant/payments/{uuid}/audit).
func (h *PaymentOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourcePayment))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []payAuditEntry{}
	for rows.Next() {
		var (
			e              payAuditEntry
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

package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
	"stonesuite-backend/workflow"
)

func invoiceSnapshot(i *invoice.Invoice) map[string]any {
	if i == nil {
		return nil
	}
	return map[string]any{
		"id":           i.ID,
		"number":       i.Number,
		"statusCode":   i.StatusCode,
		"customerId":   i.Customer.ID,
		"ownerUserId":  i.OwnerUserID,
		"grandTotal":   i.GrandTotal,
		"amountPaid":   i.AmountPaid,
		"balanceDue":   i.BalanceDue,
		"itemCount":    len(i.Items),
		"customFields": i.CustomFields,
	}
}

func auditInvoice(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, invoiceID string, oldInvoice, newInvoice *invoice.Invoice) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceInvoice), invoiceID, "invoice",
		invoiceSnapshot(oldInvoice), invoiceSnapshot(newInvoice), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("invoice: audit %s %s: %v", action, invoiceID, err)
	}
}

type invAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

func (h *InvoiceOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourceInvoice))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []invAuditEntry{}
	for rows.Next() {
		var (
			e              invAuditEntry
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

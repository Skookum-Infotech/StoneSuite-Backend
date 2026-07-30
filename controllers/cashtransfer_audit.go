package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/workflow"
)

// ctSnapshot flattens a CashTransfer into the map recorded in audit_logs.
func ctSnapshot(ct *cashtransfer.CashTransfer) map[string]any {
	if ct == nil {
		return nil
	}
	return map[string]any{
		"id":            ct.ID,
		"number":        ct.Number,
		"statusCode":    ct.StatusCode,
		"fromAccountId": ct.FromAccount.ID,
		"toAccountId":   ct.ToAccount.ID,
		"amount":        ct.Amount,
		"ownerUserId":   ct.OwnerUserID,
		"customFields":  ct.CustomFields,
	}
}

// auditCT records a create/update/delete/transition/post/reverse event for a
// cash transfer.
func auditCT(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, ctID string, oldCT, newCT *cashtransfer.CashTransfer) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceCashTransfer), ctID, "cash_transfer",
		ctSnapshot(oldCT), ctSnapshot(newCT), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("cashtransfer: audit %s %s: %v", action, ctID, err)
	}
}

// ctAuditEntry is a single row of a cash transfer's audit trail.
type ctAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

// Audit GET /api/tenant/finance/cash-transfers/{uuid}/audit
func (h *CashTransferOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authCashTransferByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourceCashTransfer))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []ctAuditEntry{}
	for rows.Next() {
		var (
			e              ctAuditEntry
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

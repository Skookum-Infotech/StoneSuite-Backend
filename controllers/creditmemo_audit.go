package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/workflow"
)

func creditMemoSnapshot(cm *creditmemo.CreditMemo) map[string]any {
	if cm == nil {
		return nil
	}
	return map[string]any{
		"id":               cm.ID,
		"number":           cm.Number,
		"statusCode":       cm.StatusCode,
		"customerId":       cm.Customer.ID,
		"ownerUserId":      cm.OwnerUserID,
		"grandTotal":       cm.GrandTotal,
		"appliedTotal":     cm.AppliedTotal,
		"unappliedAmount":  cm.UnappliedAmount,
		"lineCount":        len(cm.Lines),
		"applicationCount": len(cm.Applications),
		"customFields":     cm.CustomFields,
	}
}

// auditCreditMemo writes an audit_logs row. actorUserID is "" (-> NULL) and the
// employee id goes into the details JSONB, per the DESIGN NOTE on audit_logs:
// actor_user_id references the v1 UUID users table, while the v2 CRM uses
// INTEGER employee ids.
//
// Audit failure is logged, never fatal — losing the trail must not fail the
// business operation that already committed.
func auditCreditMemo(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, creditMemoID string, oldCM, newCM *creditmemo.CreditMemo) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceCreditMemo), creditMemoID, "credit_memo",
		creditMemoSnapshot(oldCM), creditMemoSnapshot(newCM), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("creditmemo: audit %s %s: %v", action, creditMemoID, err)
	}
}

type cmAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

func (h *CreditMemoOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authCreditMemoByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourceCreditMemo))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []cmAuditEntry{}
	for rows.Next() {
		var (
			e              cmAuditEntry
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

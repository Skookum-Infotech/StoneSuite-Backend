package controllers

import (
	"encoding/json"
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

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

package controllers

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// appVersion stamps audit rows with the running build; set via APP_VERSION.
var appVersion = func() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return "dev"
}()

// clientIP extracts the caller's IP, honouring a reverse proxy's
// X-Forwarded-For / X-Real-IP before falling back to the socket address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		candidate := strings.TrimSpace(strings.Split(xff, ",")[0])
		if net.ParseIP(candidate) != nil {
			return candidate
		}
		// Malformed X-Forwarded-For value — fall through to RemoteAddr.
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		candidate := strings.TrimSpace(xr)
		if net.ParseIP(candidate) != nil {
			return candidate
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// recordSnapshot flattens a record into a JSON-able map for the audit trail.
func recordSnapshot(rec *workflow.Record) map[string]any {
	if rec == nil {
		return nil
	}
	return map[string]any{
		"id":             rec.ID,
		"recordType":     rec.WorkflowID,
		"recordNumber":   rec.RecordNumber,
		"currentStateId": rec.CurrentStateID,
		"ownerUserId":    rec.OwnerUserID,
		"coreFields":     rec.CoreFields,
		"customFields":   rec.CustomFields,
	}
}

// auditCRM records a CRM mutation in the unified audit_logs table. Best-effort:
// failures are logged, never returned, so auditing cannot break the request.
func auditCRM(r *http.Request, pool *pgxpool.Pool, identityID, action, resource, recordID string, oldRec, newRec *workflow.Record) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, resource, recordID, "customer",
		recordSnapshot(oldRec), recordSnapshot(newRec),
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("crm: audit %s %s/%s: %v", action, resource, recordID, err)
	}
}

// auditEntry is one row of a record's audit trail in API form.
type auditEntry struct {
	Action      string         `json:"action"`
	Resource    string         `json:"resource"`
	ActorUserID string         `json:"actorUserId"`
	IPAddress   string         `json:"ipAddress"`
	AppVersion  string         `json:"appVersion"`
	OldValue    map[string]any `json:"oldValue,omitempty"`
	NewValue    map[string]any `json:"newValue,omitempty"`
	At          time.Time      `json:"at"`
}

// RecordAudit GET /api/tenant/crm/{workflowKey}/records/{id}/audit
// Returns the unified audit trail for a single CRM record (most recent first).
func (h *CRMOps) RecordAudit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, pool, _, _, ok := h.authCRMByRecordID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT action, resource, COALESCE(actor_user_id::text,''),
		       COALESCE(host(ip_address),''), COALESCE(app_version,''),
		       old_value, new_value, created_at
		FROM audit_logs
		WHERE resource_id = $1
		ORDER BY created_at DESC
		LIMIT 200`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []auditEntry{}
	for rows.Next() {
		var (
			e              auditEntry
			oldRaw, newRaw []byte
		)
		if err := rows.Scan(&e.Action, &e.Resource, &e.ActorUserID,
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

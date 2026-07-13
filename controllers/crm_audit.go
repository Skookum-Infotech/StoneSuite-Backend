package controllers

import (
	"context"
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
		recordSnapshot(oldRec), recordSnapshot(newRec), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("crm: audit %s %s/%s: %v", action, resource, recordID, err)
	}
}

// auditCRMDelete is the delete-specific variant that stores the user-supplied
// reason in the details JSONB alongside the before-snapshot.
func auditCRMDelete(r *http.Request, pool *pgxpool.Pool, identityID, resource, recordID, reason string, oldRec *workflow.Record) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	meta := map[string]any{"reason": reason}
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", resource, recordID, "customer",
		recordSnapshot(oldRec), nil, meta,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("crm: audit delete %s/%s: %v", resource, recordID, err)
	}
}

// auditEntry is one row of a record's audit trail in API form.
type auditEntry struct {
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	ActorName string         `json:"actorName"`
	IPAddress string         `json:"ipAddress"`
	AppVersion string        `json:"appVersion"`
	OldValue  map[string]any `json:"oldValue,omitempty"`
	NewValue  map[string]any `json:"newValue,omitempty"`
	At        time.Time      `json:"at"`
}

// loadAuditEntries reads the unified audit_logs trail for a single record
// (most recent first, capped at 200), shared by every resource's audit
// endpoint (CRM records, Sales Orders, ...) so the scan/unmarshal logic lives
// in one place.
func loadAuditEntries(ctx context.Context, pool *pgxpool.Pool, recordID string) ([]auditEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT al.action, al.resource,
		       COALESCE(u.full_name, u.email, ''),
		       COALESCE(host(al.ip_address),''), COALESCE(al.app_version,''),
		       al.old_value, al.new_value, al.created_at
		FROM audit_logs al
		LEFT JOIN users u ON u.id = al.actor_user_id
		WHERE al.resource_id = $1
		ORDER BY al.created_at DESC
		LIMIT 200`, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []auditEntry{}
	for rows.Next() {
		var (
			e              auditEntry
			oldRaw, newRaw []byte
		)
		if err := rows.Scan(&e.Action, &e.Resource, &e.ActorName,
			&e.IPAddress, &e.AppVersion, &oldRaw, &newRaw, &e.At); err != nil {
			return nil, err
		}
		if len(oldRaw) > 0 {
			_ = json.Unmarshal(oldRaw, &e.OldValue)
		}
		if len(newRaw) > 0 {
			_ = json.Unmarshal(newRaw, &e.NewValue)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// RecordAudit GET /api/tenant/crm/{workflowKey}/records/{id}/audit
// Returns the unified audit trail for a single CRM record (most recent first).
func (h *CRMOps) RecordAudit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, pool, _, _, ok := h.authCRMByRecordID(w, r, id, authz.ActionRead)
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

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// AuditObserver is told about every audit row LogAuditFull writes, after the
// insert succeeded. It is how one write hook re-indexes every module for the
// AI assistant without each module's controller knowing about RAG. It must be
// best-effort and never panic: the primary operation has already committed.
type AuditObserver func(ctx context.Context, q Querier, action, resource, resourceID string)

var auditObserver atomic.Pointer[AuditObserver]

// SetAuditObserver installs the observer once at boot, before the server takes
// traffic (nil clears it). It is process-wide because audit writes carry no
// dependency-injection seam.
func SetAuditObserver(o AuditObserver) {
	if o == nil {
		auditObserver.Store(nil)
		return
	}
	auditObserver.Store(&o)
}

// LogAuditFull writes one enriched row to the unified audit_logs table (the
// columns added by tenant migration 020): a before/after change record with
// request provenance. Like LogAudit it is best-effort — callers log a failure
// but never surface it to the client, because auditing must not break the
// primary operation.
//
// oldVal/newVal are marshalled to JSONB (nil → SQL NULL). ipAddress is cast to
// INET; an empty string stores NULL. tableName names the mutated table (e.g.
// "customer") so the trail is filterable per entity. extraMeta keys are merged
// into the details JSONB alongside "new" (e.g. {"reason": "..."} for deletes).
func LogAuditFull(
	ctx context.Context, q Querier,
	actorUserID, action, resource, resourceID, tableName string,
	oldVal, newVal map[string]any,
	extraMeta map[string]any,
	ipAddress, sessionID, appVersion string,
) error {
	var oldRaw, newRaw any
	if oldVal != nil {
		b, _ := json.Marshal(oldVal)
		oldRaw = b
	}
	if newVal != nil {
		b, _ := json.Marshal(newVal)
		newRaw = b
	}
	detailsMap := map[string]any{"new": newVal}
	for k, v := range extraMeta {
		detailsMap[k] = v
	}
	details, _ := json.Marshal(detailsMap)
	_, err := q.Exec(ctx, `
		INSERT INTO audit_logs (
			actor_user_id, action, resource, resource_id, details,
			table_name, old_value, new_value, ip_address, session_id, app_version)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7::jsonb,$8::jsonb,$9::inet,$10,$11)`,
		nullIfEmpty(actorUserID), action, resource, resourceID, details,
		nullIfEmpty(tableName), oldRaw, newRaw, nullIfEmpty(ipAddress),
		nullIfEmpty(sessionID), nullIfEmpty(appVersion))
	if err != nil {
		return fmt.Errorf("log audit (full): %w", err)
	}
	if o := auditObserver.Load(); o != nil {
		(*o)(ctx, q, action, resource, resourceID)
	}
	return nil
}

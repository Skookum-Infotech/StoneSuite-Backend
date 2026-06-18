package workflow

import (
	"context"
	"encoding/json"
	"fmt"
)

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
	return nil
}

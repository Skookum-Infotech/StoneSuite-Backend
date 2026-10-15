package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// AuditEvent describes one audit row LogAuditFull wrote.
type AuditEvent struct {
	Action      string
	Resource    string
	ResourceID  string
	ActorUserID string // empty for system writes (e.g. the automation engine)
}

// AuditObserver is told about every audit row LogAuditFull writes, after the
// insert succeeded. It is how one write hook re-indexes every module for the
// AI assistant (and feeds the automation outbox) without each module's
// controller knowing about either. It must be best-effort: the primary
// operation has already committed. A panic is recovered and logged.
type AuditObserver func(ctx context.Context, q Querier, ev AuditEvent)

// auditObservers is copy-on-write so LogAuditFull never takes a lock.
var auditObservers atomic.Pointer[[]AuditObserver]

// RegisterAuditObserver adds an observer at boot, before the server takes
// traffic. It is process-wide because audit writes carry no
// dependency-injection seam. A nil observer is ignored.
func RegisterAuditObserver(o AuditObserver) {
	if o == nil {
		return
	}
	for {
		old := auditObservers.Load()
		var next []AuditObserver
		if old != nil {
			next = append(next, *old...)
		}
		next = append(next, o)
		if auditObservers.CompareAndSwap(old, &next) {
			return
		}
	}
}

// resetAuditObservers clears the registry; tests only.
func resetAuditObservers() { auditObservers.Store(nil) }

// notifyAuditObservers runs every observer, isolating panics so one
// misbehaving observer cannot starve the others or fail the write.
func notifyAuditObservers(ctx context.Context, q Querier, ev AuditEvent) {
	obs := auditObservers.Load()
	if obs == nil {
		return
	}
	for _, o := range *obs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("audit observer panicked", "panic", r, "resource", ev.Resource, "action", ev.Action)
				}
			}()
			o(ctx, q, ev)
		}()
	}
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
	notifyAuditObservers(ctx, q, AuditEvent{
		Action: action, Resource: resource, ResourceID: resourceID, ActorUserID: actorUserID,
	})
	return nil
}

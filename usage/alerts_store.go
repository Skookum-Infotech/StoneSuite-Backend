package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Snooze durations accepted by AckAlert (0 means a plain acknowledge).
const (
	snoozeShortDays = 7
	snoozeLongDays  = 30
)

// alertColumns is the shared SELECT list for Alert rows (joined with tenants as t).
const alertColumns = `a.id::text, a.tenant_id::text, t.display_name, a.rule_key, a.metric_key,
	a.severity, a.status, a.message, a.detail, a.observed::float8, a.baseline::float8,
	a.first_seen_at, a.last_seen_at, a.snoozed_until`

// alertWatchPredicate selects alerts that belong on the watch-list: $1 is now.
const alertWatchPredicate = `(a.status = 'open' OR (a.status = 'snoozed' AND a.snoozed_until <= $1))`

// RecordFiring merges a rule firing into the live alert for (tenant, rule, metric), or opens a new one.
func (s *FleetStore) RecordFiring(ctx context.Context, f AlertFiring, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tenant_usage_alerts SET
		    severity = $4, observed = $5, baseline = $6, message = $7, detail = $8,
		    last_seen_at = $9,
		    status = CASE WHEN status = 'snoozed' AND snoozed_until <= $9 THEN 'open' ELSE status END,
		    snoozed_until = CASE WHEN status = 'snoozed' AND snoozed_until <= $9 THEN NULL ELSE snoozed_until END
		WHERE tenant_id = $1 AND rule_key = $2 AND metric_key = $3 AND status <> 'resolved'`,
		f.TenantID, f.RuleKey, f.MetricKey, f.Severity, f.Observed, f.Baseline, f.Message, f.Detail, now)
	if err != nil {
		return fmt.Errorf("update usage alert for tenant %s: %w", f.TenantID, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tenant_usage_alerts
		    (tenant_id, rule_key, metric_key, severity, status, observed, baseline, message, detail,
		     first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4, 'open', $5, $6, $7, $8, $9, $9)`,
		f.TenantID, f.RuleKey, f.MetricKey, f.Severity, f.Observed, f.Baseline, f.Message, f.Detail, now)
	if err != nil {
		return fmt.Errorf("insert usage alert for tenant %s: %w", f.TenantID, err)
	}
	return nil
}

// ResolveQuiet resolves the tenant's live alerts last seen before quietSince and returns how many.
func (s *FleetStore) ResolveQuiet(ctx context.Context, tenantID string, quietSince, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tenant_usage_alerts SET status = 'resolved', resolved_at = $3
		WHERE tenant_id = $1 AND status <> 'resolved' AND last_seen_at < $2`,
		tenantID, quietSince, now)
	if err != nil {
		return 0, fmt.Errorf("resolve quiet alerts for tenant %s: %w", tenantID, err)
	}
	return tag.RowsAffected(), nil
}

// Watchlist returns open alerts and snoozed alerts whose snooze has expired, most severe then newest first.
func (s *FleetStore) Watchlist(ctx context.Context, now time.Time) ([]Alert, error) {
	return scanAlerts(s.pool.Query(ctx, `
		SELECT `+alertColumns+`
		FROM tenant_usage_alerts a JOIN tenants t ON t.id = a.tenant_id
		WHERE `+alertWatchPredicate+`
		ORDER BY `+severityRank+`, a.last_seen_at DESC`, now))
}

// severityRank orders critical, warning, info.
const severityRank = `CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END`

// TenantAlerts returns the tenant's alerts of any status first seen at or after since, newest first.
func (s *FleetStore) TenantAlerts(ctx context.Context, tenantID string, since time.Time) ([]Alert, error) {
	return scanAlerts(s.pool.Query(ctx, `
		SELECT `+alertColumns+`
		FROM tenant_usage_alerts a JOIN tenants t ON t.id = a.tenant_id
		WHERE a.tenant_id = $1 AND a.first_seen_at >= $2
		ORDER BY a.first_seen_at DESC`, tenantID, since))
}

// TopSeverityByTenant maps tenant id to its most severe watch-list alert severity.
func (s *FleetStore) TopSeverityByTenant(ctx context.Context, now time.Time) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (a.tenant_id) a.tenant_id::text, a.severity
		FROM tenant_usage_alerts a
		WHERE `+alertWatchPredicate+`
		ORDER BY a.tenant_id, `+severityRank, now)
	if err != nil {
		return nil, fmt.Errorf("read top alert severity by tenant: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, sev string
		if err := rows.Scan(&id, &sev); err != nil {
			return nil, fmt.Errorf("scan top severity row: %w", err)
		}
		out[id] = sev
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate top severity rows: %w", err)
	}
	return out, nil
}

// AckAlert acknowledges (snoozeDays 0) or snoozes (7 or 30 days) a live alert; false when no live alert has that id.
func (s *FleetStore) AckAlert(ctx context.Context, alertID, actorIdentityID string, snoozeDays int, now time.Time) (bool, error) {
	status := AlertStatusAcked
	var snoozedUntil *time.Time
	switch snoozeDays {
	case 0:
	case snoozeShortDays, snoozeLongDays:
		status = AlertStatusSnoozed
		until := now.AddDate(0, 0, snoozeDays)
		snoozedUntil = &until
	default:
		return false, fmt.Errorf("ack alert %s: unsupported snooze days %d", alertID, snoozeDays)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tenant_usage_alerts
		SET status = $2, snoozed_until = $3, acted_by = $4, acted_at = $5
		WHERE id = $1 AND status <> 'resolved'`,
		alertID, status, snoozedUntil, actorIdentityID, now)
	if err != nil {
		return false, fmt.Errorf("ack alert %s: %w", alertID, err)
	}
	return tag.RowsAffected() > 0, nil
}

func scanAlerts(rows pgx.Rows, err error) ([]Alert, error) {
	if err != nil {
		return nil, fmt.Errorf("query usage alerts: %w", err)
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.TenantID, &a.TenantName, &a.RuleKey, &a.MetricKey,
			&a.Severity, &a.Status, &a.Message, &a.Detail, &a.Observed, &a.Baseline,
			&a.FirstSeenAt, &a.LastSeenAt, &a.SnoozedUntil); err != nil {
			return nil, fmt.Errorf("scan usage alert row: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage alert rows: %w", err)
	}
	return out, nil
}

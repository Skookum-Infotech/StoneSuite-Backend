package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// HourlyTotals is a tenant's request metering summed over a window.
// P95Ms is the maximum hourly p95 in the window, an approximation of the true p95.
type HourlyTotals struct {
	Requests, Errors5xx, HandlerMs int64
	P95Ms                          int
}

// JobStats summarises a tenant's finished async jobs over a window.
type JobStats struct {
	JobMs  int64
	Failed int64
	Dead   int64
}

// SignInStats summarises a tenant's user activity.
type SignInStats struct {
	ActiveUsers  int
	TotalUsers   int
	LastSignInAt *time.Time
}

// HourlyTotals sums the tenant's hourly request rows with hour in [from, to).
func (s *FleetStore) HourlyTotals(ctx context.Context, tenantID string, from, to time.Time) (HourlyTotals, error) {
	var t HourlyTotals
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(requests), 0)::BIGINT, COALESCE(SUM(errors_5xx), 0)::BIGINT,
		       COALESCE(SUM(handler_ms), 0)::BIGINT, COALESCE(MAX(p95_ms), 0)::INT
		FROM tenant_request_hourly
		WHERE tenant_id = $1 AND hour >= $2 AND hour < $3`,
		tenantID, from, to).Scan(&t.Requests, &t.Errors5xx, &t.HandlerMs, &t.P95Ms)
	if err != nil {
		return HourlyTotals{}, fmt.Errorf("read hourly totals for tenant %s: %w", tenantID, err)
	}
	return t, nil
}

// JobStats reads the tenant's async jobs last updated in [from, to).
func (s *FleetStore) JobStats(ctx context.Context, tenantID string, from, to time.Time) (JobStats, error) {
	var j JobStats
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(EXTRACT(EPOCH FROM (updated_at - created_at)) * 1000)
		           FILTER (WHERE status IN ('succeeded', 'failed', 'dead')), 0)::BIGINT,
		       COUNT(*) FILTER (WHERE status = 'failed'),
		       COUNT(*) FILTER (WHERE status = 'dead')
		FROM async_jobs
		WHERE tenant_id = $1 AND updated_at >= $2 AND updated_at < $3`,
		tenantID, from, to).Scan(&j.JobMs, &j.Failed, &j.Dead)
	if err != nil {
		return JobStats{}, fmt.Errorf("read job stats for tenant %s: %w", tenantID, err)
	}
	return j, nil
}

// SignInStats counts the tenant's active and total users and its latest sign-in.
func (s *FleetStore) SignInStats(ctx context.Context, tenantID string, from, to time.Time) (SignInStats, error) {
	var st SignInStats
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT a.identity_id) FILTER (WHERE a.last_seen_at >= $2 AND a.last_seen_at < $3),
		       (SELECT COUNT(*) FROM identities WHERE tenant_id = $1),
		       MAX(a.created_at)
		FROM identities i
		JOIN auth_sessions a ON a.identity_id = i.id
		WHERE i.tenant_id = $1`,
		tenantID, from, to).Scan(&st.ActiveUsers, &st.TotalUsers, &st.LastSignInAt)
	if err != nil {
		return SignInStats{}, fmt.Errorf("read sign-in stats for tenant %s: %w", tenantID, err)
	}
	return st, nil
}

// SignInStatsByTenant is SignInStats for many tenants in one query, keyed by
// tenant id; a tenant with no identities is absent (its zero value applies).
func (s *FleetStore) SignInStatsByTenant(ctx context.Context, tenantIDs []string, from, to time.Time) (map[string]SignInStats, error) {
	out := make(map[string]SignInStats, len(tenantIDs))
	if len(tenantIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT i.tenant_id::text,
		       COUNT(DISTINCT a.identity_id) FILTER (WHERE a.last_seen_at >= $2 AND a.last_seen_at < $3),
		       COUNT(DISTINCT i.id),
		       MAX(a.created_at)
		FROM identities i
		LEFT JOIN auth_sessions a ON a.identity_id = i.id
		WHERE i.tenant_id = ANY($1::uuid[])
		GROUP BY i.tenant_id`,
		tenantIDs, from, to)
	if err != nil {
		return nil, fmt.Errorf("read sign-in stats for %d tenants: %w", len(tenantIDs), err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var st SignInStats
		if err := rows.Scan(&id, &st.ActiveUsers, &st.TotalUsers, &st.LastSignInAt); err != nil {
			return nil, fmt.Errorf("scan sign-in stats row: %w", err)
		}
		out[id] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sign-in stats rows: %w", err)
	}
	return out, nil
}

// DailyValues returns the tenants' daily metric values for days in [from, to] inclusive.
func (s *FleetStore) DailyValues(ctx context.Context, tenantIDs []string, from, to time.Time) ([]DailyValue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id::text, usage_date, metric_key, value::float8
		FROM tenant_usage_daily
		WHERE tenant_id = ANY($1::uuid[]) AND usage_date >= $2::date AND usage_date <= $3::date
		ORDER BY usage_date, tenant_id, metric_key`,
		tenantIDs, from, to)
	if err != nil {
		return nil, fmt.Errorf("read daily values: %w", err)
	}
	return collectDailyValues(rows, "daily value")
}

// TokensByDay returns the tenants' summed prompt+completion tokens per day (Key = ai_tokens), days inclusive.
func (s *FleetStore) TokensByDay(ctx context.Context, tenantIDs []string, from, to time.Time) ([]DailyValue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id::text, usage_date, $4::text, SUM(prompt_tokens + completion_tokens)::float8
		FROM tenant_llm_usage_daily
		WHERE tenant_id = ANY($1::uuid[]) AND usage_date >= $2::date AND usage_date <= $3::date
		GROUP BY tenant_id, usage_date
		ORDER BY usage_date, tenant_id`,
		tenantIDs, from, to, MetricAITokens)
	if err != nil {
		return nil, fmt.Errorf("read token usage by day: %w", err)
	}
	return collectDailyValues(rows, "token usage")
}

func collectDailyValues(rows pgx.Rows, what string) ([]DailyValue, error) {
	defer rows.Close()
	out := []DailyValue{}
	for rows.Next() {
		var v DailyValue
		if err := rows.Scan(&v.TenantID, &v.Day, &v.Key, &v.Value); err != nil {
			return nil, fmt.Errorf("scan %s row: %w", what, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s rows: %w", what, err)
	}
	return out, nil
}

// TokensBySource sums the tenant's tokens per source for days in [from, to] inclusive.
func (s *FleetStore) TokensBySource(ctx context.Context, tenantID string, from, to time.Time) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source, SUM(prompt_tokens + completion_tokens)::BIGINT
		FROM tenant_llm_usage_daily
		WHERE tenant_id = $1 AND usage_date >= $2::date AND usage_date <= $3::date
		GROUP BY source`,
		tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("read tokens by source for tenant %s: %w", tenantID, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var src string
		var n int64
		if err := rows.Scan(&src, &n); err != nil {
			return nil, fmt.Errorf("scan tokens by source row: %w", err)
		}
		out[src] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tokens by source rows: %w", err)
	}
	return out, nil
}

// TableSizes returns the tenant's table sizes, largest first, with the latest measurement time (nil when none).
func (s *FleetStore) TableSizes(ctx context.Context, tenantID string) ([]TableSize, *time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT table_name, total_bytes, row_estimate, measured_at
		FROM tenant_table_sizes
		WHERE tenant_id = $1
		ORDER BY total_bytes DESC, table_name`, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("read table sizes for tenant %s: %w", tenantID, err)
	}
	defer rows.Close()
	out := []TableSize{}
	var measured *time.Time
	for rows.Next() {
		var sz TableSize
		var at time.Time
		if err := rows.Scan(&sz.TableName, &sz.TotalBytes, &sz.RowEstimate, &at); err != nil {
			return nil, nil, fmt.Errorf("scan table size row: %w", err)
		}
		if measured == nil || at.After(*measured) {
			a := at
			measured = &a
		}
		out = append(out, sz)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate table size rows: %w", err)
	}
	return out, measured, nil
}

// LatestRolledUpDay returns the newest usage_date in tenant_usage_daily, or nil when nothing is rolled up.
func (s *FleetStore) LatestRolledUpDay(ctx context.Context) (*time.Time, error) {
	var d *time.Time
	err := s.pool.QueryRow(ctx, `SELECT MAX(usage_date) FROM tenant_usage_daily`).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest rolled-up day: %w", err)
	}
	return d, nil
}

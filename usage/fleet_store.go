package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention windows enforced by PruneRetention.
const (
	hourlyRetentionDays  = 90
	dailyRetentionMonths = 13
	alertRetentionDays   = 90
)

// FleetStore reads and writes the fleet-wide control-plane usage tables.
// Every query filters by tenant_id explicitly.
type FleetStore struct {
	pool *pgxpool.Pool
}

// NewFleetStore wraps the control-plane pool.
func NewFleetStore(pool *pgxpool.Pool) *FleetStore { return &FleetStore{pool: pool} }

// UpsertRequestHourly merges hourly request rows in one batch, adding counters and keeping the max p95.
func (s *FleetStore) UpsertRequestHourly(ctx context.Context, rows []HourlyRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO tenant_request_hourly
			    (tenant_id, hour, requests, errors_5xx, handler_ms, p95_ms)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, hour) DO UPDATE SET
			    requests   = tenant_request_hourly.requests + EXCLUDED.requests,
			    errors_5xx = tenant_request_hourly.errors_5xx + EXCLUDED.errors_5xx,
			    handler_ms = tenant_request_hourly.handler_ms + EXCLUDED.handler_ms,
			    p95_ms     = GREATEST(tenant_request_hourly.p95_ms, EXCLUDED.p95_ms),
			    updated_at = NOW()`,
			r.TenantID, r.Hour, r.Requests, r.Errors5xx, r.HandlerMs, r.P95Ms)
	}
	res := s.pool.SendBatch(ctx, batch)
	for range rows {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return fmt.Errorf("upsert request hourly: %w", err)
		}
	}
	if err := res.Close(); err != nil {
		return fmt.Errorf("close request hourly batch: %w", err)
	}
	return nil
}

// UpsertDailyMetrics replaces the tenant's metric values for one UTC day (idempotent).
func (s *FleetStore) UpsertDailyMetrics(ctx context.Context, tenantID string, day time.Time, values map[string]float64) error {
	if len(values) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for key, v := range values {
		batch.Queue(`
			INSERT INTO tenant_usage_daily (tenant_id, usage_date, metric_key, value)
			VALUES ($1, $2::date, $3, $4)
			ON CONFLICT (tenant_id, usage_date, metric_key) DO UPDATE SET
			    value = EXCLUDED.value, updated_at = NOW()`,
			tenantID, day, key, v)
	}
	res := s.pool.SendBatch(ctx, batch)
	for range values {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return fmt.Errorf("upsert daily metrics for tenant %s: %w", tenantID, err)
		}
	}
	if err := res.Close(); err != nil {
		return fmt.Errorf("close daily metrics batch for tenant %s: %w", tenantID, err)
	}
	return nil
}

// ReplaceTableSizes swaps the tenant's table-size rows for the new measurement in one transaction.
func (s *FleetStore) ReplaceTableSizes(ctx context.Context, tenantID string, measuredAt time.Time, sizes []TableSize) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin table sizes tx for tenant %s: %w", tenantID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM tenant_table_sizes WHERE tenant_id = $1`, tenantID); err != nil {
		return fmt.Errorf("delete table sizes for tenant %s: %w", tenantID, err)
	}
	for _, sz := range sizes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenant_table_sizes (tenant_id, table_name, total_bytes, row_estimate, measured_at)
			VALUES ($1, $2, $3, $4, $5)`,
			tenantID, sz.TableName, sz.TotalBytes, sz.RowEstimate, measuredAt); err != nil {
			return fmt.Errorf("insert table size %s for tenant %s: %w", sz.TableName, tenantID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit table sizes for tenant %s: %w", tenantID, err)
	}
	return nil
}

// PruneRetention deletes hourly rows over 90 days, daily rows over 13 months and resolved alerts over 90 days old.
func (s *FleetStore) PruneRetention(ctx context.Context, now time.Time) error {
	stmts := []struct {
		name, sql string
		cutoff    time.Time
	}{
		{"hourly", `DELETE FROM tenant_request_hourly WHERE hour < $1`, now.AddDate(0, 0, -hourlyRetentionDays)},
		{"daily", `DELETE FROM tenant_usage_daily WHERE usage_date < $1::date`, now.AddDate(0, -dailyRetentionMonths, 0)},
		{"alerts", `DELETE FROM tenant_usage_alerts WHERE status = 'resolved' AND resolved_at < $1`, now.AddDate(0, 0, -alertRetentionDays)},
	}
	for _, st := range stmts {
		if _, err := s.pool.Exec(ctx, st.sql, st.cutoff); err != nil {
			return fmt.Errorf("prune %s usage rows: %w", st.name, err)
		}
	}
	return nil
}

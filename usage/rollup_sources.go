package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit actions counted by the rollup.
const (
	auditActionCreate       = "create"
	auditActionDocumentSent = "document.sent"
)

// Automation delivery statuses (automation_deliveries.status).
const (
	deliveryStatusSent    = "sent"
	deliveryStatusSkipped = "skipped"
	deliveryStatusFailed  = "failed"
	deliveryStatusDead    = "dead"
	deliveryStatusUnknown = "unknown"
)

// topTableLimit is how many of the largest tables are recorded per tenant.
const topTableLimit = 10

// terminalDeliveryStatuses are the statuses a delivery can no longer leave without
// operator action; planned and claimed rows are still in flight and do not count as runs.
var terminalDeliveryStatuses = []string{
	deliveryStatusSent, deliveryStatusSkipped, deliveryStatusFailed, deliveryStatusDead, deliveryStatusUnknown,
}

// tenantSource reads one tenant database's usage figures (pgTenantSource in production).
type tenantSource interface {
	DBSize(ctx context.Context) (int64, error)
	TopTables(ctx context.Context) ([]TableSize, error)
	RecordsCreated(ctx context.Context, from, to time.Time) (map[string]int64, error)
	DocumentsSent(ctx context.Context, from, to time.Time) (int64, error)
	Deliveries(ctx context.Context, from, to time.Time) (runs, sent int64, err error)
}

// pgTenantSource implements tenantSource over a single tenant's own pool.
type pgTenantSource struct{ pool *pgxpool.Pool }

// newPGTenantSource wraps one tenant's pool; it never sees another tenant's database.
func newPGTenantSource(pool *pgxpool.Pool) tenantSource { return &pgTenantSource{pool: pool} }

// DBSize returns the size in bytes of the tenant database.
func (s *pgTenantSource) DBSize(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&n); err != nil {
		return 0, fmt.Errorf("read database size: %w", err)
	}
	return n, nil
}

// TopTables returns the largest public tables with their total size and estimated rows.
func (s *pgTenantSource) TopTables(ctx context.Context) ([]TableSize, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.relname, pg_total_relation_size(c.oid)::bigint, GREATEST(c.reltuples, 0)::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = 'public'
		ORDER BY pg_total_relation_size(c.oid) DESC, c.relname
		LIMIT $1`, topTableLimit)
	if err != nil {
		return nil, fmt.Errorf("query top tables: %w", err)
	}
	defer rows.Close()
	out := []TableSize{}
	for rows.Next() {
		var sz TableSize
		if err := rows.Scan(&sz.TableName, &sz.TotalBytes, &sz.RowEstimate); err != nil {
			return nil, fmt.Errorf("scan top table row: %w", err)
		}
		out = append(out, sz)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate top table rows: %w", err)
	}
	return out, nil
}

// RecordsCreated counts audited create actions per resource in [from, to).
func (s *pgTenantSource) RecordsCreated(ctx context.Context, from, to time.Time) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT resource, COUNT(*) FROM audit_logs
		WHERE action = $3 AND created_at >= $1 AND created_at < $2
		GROUP BY resource`, from, to, auditActionCreate)
	if err != nil {
		return nil, fmt.Errorf("query records created: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var resource string
		var n int64
		if err := rows.Scan(&resource, &n); err != nil {
			return nil, fmt.Errorf("scan records created row: %w", err)
		}
		out[resource] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate records created rows: %w", err)
	}
	return out, nil
}

// DocumentsSent counts document.sent audit entries in [from, to).
func (s *pgTenantSource) DocumentsSent(ctx context.Context, from, to time.Time) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_logs
		WHERE action = $3 AND created_at >= $1 AND created_at < $2`,
		from, to, auditActionDocumentSent).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count documents sent: %w", err)
	}
	return n, nil
}

// Deliveries counts automation deliveries due in [from, to): runs reached a terminal status, sent were delivered.
func (s *pgTenantSource) Deliveries(ctx context.Context, from, to time.Time) (int64, int64, error) {
	var runs, sent int64
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE status = ANY($3)),
		       COUNT(*) FILTER (WHERE status = $4)
		FROM automation_deliveries
		WHERE run_at >= $1 AND run_at < $2`,
		from, to, terminalDeliveryStatuses, deliveryStatusSent).Scan(&runs, &sent)
	if err != nil {
		return 0, 0, fmt.Errorf("count automation deliveries: %w", err)
	}
	return runs, sent, nil
}

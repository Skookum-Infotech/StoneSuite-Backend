package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenEntry is one metered LLM call, attributed to a tenant and UTC day.
type TokenEntry struct {
	TenantID         string
	Day              time.Time
	Source           Source
	Model            string
	PromptTokens     int64
	CompletionTokens int64
}

// SourceTokens is one row of the per-source breakdown.
type SourceTokens struct {
	Source           Source
	PromptTokens     int64
	CompletionTokens int64
}

// TokenTotals is a tenant's token consumption over a period.
type TokenTotals struct {
	PromptTokens     int64
	CompletionTokens int64
	BySource         []SourceTokens
}

// StorageSnapshot is the latest measured size of a tenant's R2 bucket.
type StorageSnapshot struct {
	UsedBytes   int64
	ObjectCount int64
	MeasuredAt  time.Time
}

// Store reads and writes the control-plane usage tables. Every query filters
// by tenant_id explicitly — the control plane is shared across tenants.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the control-plane pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// RecordTokens adds one call's tokens to the tenant's daily rollup row.
func (s *Store) RecordTokens(ctx context.Context, e TokenEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_llm_usage_daily
		    (tenant_id, usage_date, source, model, prompt_tokens, completion_tokens, calls)
		VALUES ($1, $2::date, $3, $4, $5, $6, 1)
		ON CONFLICT (tenant_id, usage_date, source, model) DO UPDATE SET
		    prompt_tokens     = tenant_llm_usage_daily.prompt_tokens + EXCLUDED.prompt_tokens,
		    completion_tokens = tenant_llm_usage_daily.completion_tokens + EXCLUDED.completion_tokens,
		    calls             = tenant_llm_usage_daily.calls + 1,
		    updated_at        = NOW()`,
		e.TenantID, e.Day, string(e.Source), e.Model, e.PromptTokens, e.CompletionTokens)
	if err != nil {
		return fmt.Errorf("record llm usage for tenant %s: %w", e.TenantID, err)
	}
	return nil
}

// SumTokens totals the tenant's tokens for days in [p.Start, p.End), largest
// source first.
func (s *Store) SumTokens(ctx context.Context, tenantID string, p Period) (TokenTotals, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source, SUM(prompt_tokens)::BIGINT, SUM(completion_tokens)::BIGINT
		FROM tenant_llm_usage_daily
		WHERE tenant_id = $1 AND usage_date >= $2::date AND usage_date < $3::date
		GROUP BY source
		ORDER BY SUM(prompt_tokens + completion_tokens) DESC, source`,
		tenantID, p.Start, p.End)
	if err != nil {
		return TokenTotals{}, fmt.Errorf("sum llm usage for tenant %s: %w", tenantID, err)
	}
	defer rows.Close()
	totals := TokenTotals{BySource: []SourceTokens{}}
	for rows.Next() {
		var row SourceTokens
		var src string
		if err := rows.Scan(&src, &row.PromptTokens, &row.CompletionTokens); err != nil {
			return TokenTotals{}, fmt.Errorf("scan llm usage row: %w", err)
		}
		row.Source = Source(src)
		totals.PromptTokens += row.PromptTokens
		totals.CompletionTokens += row.CompletionTokens
		totals.BySource = append(totals.BySource, row)
	}
	if err := rows.Err(); err != nil {
		return TokenTotals{}, fmt.Errorf("iterate llm usage rows: %w", err)
	}
	return totals, nil
}

// BillingAnchor returns the date the tenant's billing periods are anchored
// on: billing_anchor_date, or the tenant's creation date when unset.
func (s *Store) BillingAnchor(ctx context.Context, tenantID string) (time.Time, error) {
	var anchor time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(billing_anchor_date, created_at::date) FROM tenants WHERE id = $1`,
		tenantID).Scan(&anchor)
	if err != nil {
		return time.Time{}, fmt.Errorf("read billing anchor for tenant %s: %w", tenantID, err)
	}
	return anchor, nil
}

// StorageSnapshot returns the tenant's latest bucket measurement, or nil when
// it has never been measured.
func (s *Store) StorageSnapshot(ctx context.Context, tenantID string) (*StorageSnapshot, error) {
	var snap StorageSnapshot
	err := s.pool.QueryRow(ctx,
		`SELECT used_bytes, object_count, measured_at FROM tenant_storage_snapshots WHERE tenant_id = $1`,
		tenantID).Scan(&snap.UsedBytes, &snap.ObjectCount, &snap.MeasuredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read storage snapshot for tenant %s: %w", tenantID, err)
	}
	return &snap, nil
}

// UpsertStorageSnapshot replaces the tenant's bucket measurement.
func (s *Store) UpsertStorageSnapshot(ctx context.Context, tenantID string, snap StorageSnapshot) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_storage_snapshots (tenant_id, used_bytes, object_count, measured_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    used_bytes = EXCLUDED.used_bytes,
		    object_count = EXCLUDED.object_count,
		    measured_at = EXCLUDED.measured_at`,
		tenantID, snap.UsedBytes, snap.ObjectCount, snap.MeasuredAt)
	if err != nil {
		return fmt.Errorf("upsert storage snapshot for tenant %s: %w", tenantID, err)
	}
	return nil
}

//go:build dbtest

package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func controlPlanePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_CP_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func insertTenant(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO tenants (slug, display_name, created_at)
		 VALUES ('usage-' || substr(gen_random_uuid()::text, 1, 8), 'Usage Test', '2026-03-20T10:00:00Z')
		 RETURNING id::text`).Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

func TestStore_TokensRollUpAndSumWithinPeriod(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewStore(pool)
	tenant, other := insertTenant(t, pool), insertTenant(t, pool)

	entries := []TokenEntry{
		{TenantID: tenant, Day: day(2026, 11, 19), Source: SourceAsk, Model: "m", PromptTokens: 10, CompletionTokens: 5},
		{TenantID: tenant, Day: day(2026, 11, 19), Source: SourceAsk, Model: "m", PromptTokens: 20, CompletionTokens: 5},
		{TenantID: tenant, Day: day(2026, 10, 20), Source: SourceImport, Model: "m", PromptTokens: 7, CompletionTokens: 0},
		{TenantID: tenant, Day: day(2026, 10, 19), Source: SourceAsk, Model: "m", PromptTokens: 1000}, // previous period
		{TenantID: tenant, Day: day(2026, 11, 20), Source: SourceAsk, Model: "m", PromptTokens: 1000}, // next period
		{TenantID: other, Day: day(2026, 11, 1), Source: SourceAsk, Model: "m", PromptTokens: 5000},   // another tenant
	}
	for _, e := range entries {
		require.NoError(t, s.RecordTokens(ctx, e))
	}

	var calls int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT calls FROM tenant_llm_usage_daily WHERE tenant_id = $1 AND usage_date = '2026-11-19' AND source = 'ask'`,
		tenant).Scan(&calls))
	assert.EqualValues(t, 2, calls, "same day/source/model increments one row")

	anchor, err := s.BillingAnchor(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, day(2026, 3, 20), anchor.UTC(), "falls back to created_at")
	period := CurrentPeriod(anchor, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	require.Equal(t, Period{day(2026, 10, 20), day(2026, 11, 20)}, period)

	totals, err := s.SumTokens(ctx, tenant, period)
	require.NoError(t, err)
	assert.Equal(t, TokenTotals{PromptTokens: 37, CompletionTokens: 10, BySource: []SourceTokens{
		{Source: SourceAsk, PromptTokens: 30, CompletionTokens: 10},
		{Source: SourceImport, PromptTokens: 7},
	}}, totals)
}

func TestStore_StorageSnapshotUpsert(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewStore(pool)
	tenant := insertTenant(t, pool)

	snap, err := s.StorageSnapshot(ctx, tenant)
	require.NoError(t, err)
	assert.Nil(t, snap, "never measured")

	first := StorageSnapshot{UsedBytes: 100, ObjectCount: 2, MeasuredAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	second := StorageSnapshot{UsedBytes: 300, ObjectCount: 5, MeasuredAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	require.NoError(t, s.UpsertStorageSnapshot(ctx, tenant, first))
	require.NoError(t, s.UpsertStorageSnapshot(ctx, tenant, second))

	snap, err = s.StorageSnapshot(ctx, tenant)
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, second.UsedBytes, snap.UsedBytes)
	assert.Equal(t, second.ObjectCount, snap.ObjectCount)
	assert.True(t, second.MeasuredAt.Equal(snap.MeasuredAt))
}

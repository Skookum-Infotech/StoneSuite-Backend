//go:build dbtest

package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFleetStore_RequestHourlyMergesAndTotals(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant, other := insertTenant(t, pool), insertTenant(t, pool)
	h := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	require.NoError(t, s.UpsertRequestHourly(ctx, nil))
	require.NoError(t, s.UpsertRequestHourly(ctx, []HourlyRow{
		{TenantID: tenant, Hour: h, Requests: 10, Errors5xx: 1, HandlerMs: 100, P95Ms: 50},
		{TenantID: other, Hour: h, Requests: 999, P95Ms: 999},
	}))
	require.NoError(t, s.UpsertRequestHourly(ctx, []HourlyRow{
		{TenantID: tenant, Hour: h, Requests: 5, Errors5xx: 2, HandlerMs: 40, P95Ms: 30},
		{TenantID: tenant, Hour: h.Add(time.Hour), Requests: 1, P95Ms: 70},
	}))

	got, err := s.HourlyTotals(ctx, tenant, h, h.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, HourlyTotals{Requests: 16, Errors5xx: 3, HandlerMs: 140, P95Ms: 70}, got)

	first, err := s.HourlyTotals(ctx, tenant, h, h.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 15, first.Requests)
	assert.Equal(t, 50, first.P95Ms, "GREATEST keeps the larger p95")

	empty, err := s.HourlyTotals(ctx, tenant, h.Add(24*time.Hour), h.Add(48*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, HourlyTotals{}, empty)
}

func TestFleetStore_DailyMetricsReplaceAndRead(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant, other := insertTenant(t, pool), insertTenant(t, pool)
	d1, d2 := day(2026, 11, 19), day(2026, 11, 20)

	require.NoError(t, s.UpsertDailyMetrics(ctx, tenant, d1, map[string]float64{MetricRequests: 10, MetricDBBytes: 5}))
	require.NoError(t, s.UpsertDailyMetrics(ctx, tenant, d1, map[string]float64{MetricRequests: 12}))
	require.NoError(t, s.UpsertDailyMetrics(ctx, tenant, d2, map[string]float64{MetricRequests: 20}))
	require.NoError(t, s.UpsertDailyMetrics(ctx, other, d1, map[string]float64{MetricRequests: 777}))

	vals, err := s.DailyValues(ctx, []string{tenant}, d1, d2)
	require.NoError(t, err)
	require.Len(t, vals, 3)
	byKey := map[string]float64{}
	for _, v := range vals {
		assert.Equal(t, tenant, v.TenantID)
		if v.Day.Equal(d1) {
			byKey[v.Key] = v.Value
		}
	}
	assert.Equal(t, 12.0, byKey[MetricRequests], "second upsert replaces the value")
	assert.Equal(t, 5.0, byKey[MetricDBBytes])

	latest, err := s.LatestRolledUpDay(ctx)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.False(t, latest.Before(d2))
}

func TestFleetStore_TokensAndJobAndSignInStats(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s, store := NewFleetStore(pool), NewStore(pool)
	tenant := insertTenant(t, pool)
	d := day(2026, 11, 19)

	require.NoError(t, store.RecordTokens(ctx, TokenEntry{TenantID: tenant, Day: d, Source: SourceAsk, Model: "m", PromptTokens: 10, CompletionTokens: 5}))
	require.NoError(t, store.RecordTokens(ctx, TokenEntry{TenantID: tenant, Day: d, Source: SourceImport, Model: "m", PromptTokens: 7}))

	byDay, err := s.TokensByDay(ctx, []string{tenant}, d, d)
	require.NoError(t, err)
	require.Len(t, byDay, 1)
	assert.Equal(t, MetricAITokens, byDay[0].Key)
	assert.Equal(t, 22.0, byDay[0].Value)

	bySrc, err := s.TokensBySource(ctx, tenant, d, d)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{string(SourceAsk): 15, string(SourceImport): 7}, bySrc)

	start := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	for _, st := range []string{"succeeded", "failed", "dead", "pending"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO async_jobs (job_type, tenant_id, status, created_at, updated_at)
			VALUES ('usage_test', $1, $2, $3::timestamptz, $3::timestamptz + interval '2 seconds')`, tenant, st, start)
		require.NoError(t, err)
	}
	js, err := s.JobStats(ctx, tenant, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 6000, js.JobMs, "pending jobs excluded from duration")
	assert.EqualValues(t, 1, js.Failed)
	assert.EqualValues(t, 1, js.Dead)

	var identity string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO identities (tenant_id, email, password_hash)
		VALUES ($1, 'usage-' || substr(gen_random_uuid()::text, 1, 8) || '@example.test', 'x')
		RETURNING id::text`, tenant).Scan(&identity))
	_, err = pool.Exec(ctx, `
		INSERT INTO auth_sessions (identity_id, created_at, last_seen_at) VALUES ($1, $2, $3)`,
		identity, start, start.Add(time.Hour))
	require.NoError(t, err)
	ss, err := s.SignInStats(ctx, tenant, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, ss.ActiveUsers)
	assert.Equal(t, 1, ss.TotalUsers)
	require.NotNil(t, ss.LastSignInAt)
	assert.True(t, ss.LastSignInAt.Equal(start))
}

func TestFleetStore_TableSizesReplaceAndOrder(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant := insertTenant(t, pool)
	at := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

	sizes, measured, err := s.TableSizes(ctx, tenant)
	require.NoError(t, err)
	assert.Empty(t, sizes)
	assert.Nil(t, measured)

	require.NoError(t, s.ReplaceTableSizes(ctx, tenant, at, []TableSize{{"small", 10, 1}, {"big", 1000, 50}}))
	require.NoError(t, s.ReplaceTableSizes(ctx, tenant, at.Add(time.Hour), []TableSize{{"small", 20, 2}, {"big", 2000, 60}, {"mid", 500, 5}}))

	sizes, measured, err = s.TableSizes(ctx, tenant)
	require.NoError(t, err)
	require.Len(t, sizes, 3)
	assert.Equal(t, "big", sizes[0].TableName)
	assert.Equal(t, "small", sizes[2].TableName)
	require.NotNil(t, measured)
	assert.True(t, measured.Equal(at.Add(time.Hour)))
}

func TestFleetStore_PruneRetention(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant := insertTenant(t, pool)
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)

	require.NoError(t, s.UpsertRequestHourly(ctx, []HourlyRow{
		{TenantID: tenant, Hour: now.AddDate(0, 0, -91), Requests: 1},
		{TenantID: tenant, Hour: now.AddDate(0, 0, -10), Requests: 1},
	}))
	require.NoError(t, s.UpsertDailyMetrics(ctx, tenant, now.AddDate(0, -14, 0), map[string]float64{MetricRequests: 1}))
	require.NoError(t, s.UpsertDailyMetrics(ctx, tenant, now.AddDate(0, -1, 0), map[string]float64{MetricRequests: 1}))
	_, err := pool.Exec(ctx, `
		INSERT INTO tenant_usage_alerts (tenant_id, rule_key, metric_key, severity, status, resolved_at)
		VALUES ($1, 'r', 'old', 'info', 'resolved', $2), ($1, 'r', 'new', 'info', 'resolved', $3)`,
		tenant, now.AddDate(0, 0, -91), now.AddDate(0, 0, -5))
	require.NoError(t, err)

	require.NoError(t, s.PruneRetention(ctx, now))

	var hourly, daily, alerts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM tenant_request_hourly WHERE tenant_id = $1`, tenant).Scan(&hourly))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM tenant_usage_daily WHERE tenant_id = $1`, tenant).Scan(&daily))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM tenant_usage_alerts WHERE tenant_id = $1`, tenant).Scan(&alerts))
	assert.Equal(t, 1, hourly)
	assert.Equal(t, 1, daily)
	assert.Equal(t, 1, alerts)
}

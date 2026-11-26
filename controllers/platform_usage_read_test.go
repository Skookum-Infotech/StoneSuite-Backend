package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformUsage_OverviewShape(t *testing.T) {
	ops, st := newPlatUsageOps(true)
	rec := httptest.NewRecorder()
	ops.Overview(rec, platUsageReq("GET", "/api/platform/usage/overview?period=7d", "", "", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodePlatUsage(t, rec)
	requirePlatUsageKeys(t, body, "success", "generatedAt", "period", "fleet", "tenants", "nextCursor")
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "7d", body["period"])
	assert.Equal(t, "next-cursor", body["nextCursor"])
	requirePlatUsageKeys(t, body["fleet"].(map[string]any), "activeTenants", "totalTenants", "computeHours",
		"computeHoursPrev", "dbBytes", "dbBytesPrev", "r2Bytes", "r2BytesPrev", "aiTokens", "aiTokensPrev", "computeTrend")
	rows := body["tenants"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	requirePlatUsageKeys(t, row, platUsageRowKeys...)
	assert.Nil(t, row["r2Bytes"])
	assert.Equal(t, "critical", row["topSeverity"])
	assert.InDelta(t, 2.0, row["computeHours"], 1e-9)
	assert.Len(t, row["computeTrend"], 7)
	assert.InDelta(t, 1.0, row["activeUsers"], 1e-9)
	assert.InDelta(t, 3.0, row["totalUsers"], 1e-9)
	// Sign-in stats for the page come from one batched query, not one per row.
	assert.Equal(t, 1, st.signInBatchCalls)
	assert.Equal(t, []string{platUsageTenantID}, st.signInBatchIDs)
}

func TestPlatformUsage_OverviewOmitsEmptyCursorAndDefaultsPeriod(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	rec := httptest.NewRecorder()
	ops.Overview(rec, platUsageReq("GET", "/api/platform/usage/overview", "", "", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "30d", decodePlatUsage(t, rec)["period"])
}

func TestPlatformUsage_WatchlistShape(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	rec := httptest.NewRecorder()
	ops.Watchlist(rec, platUsageReq("GET", "/api/platform/usage/watchlist", "", "", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodePlatUsage(t, rec)
	alerts := body["alerts"].([]any)
	require.Len(t, alerts, 1)
	a := alerts[0].(map[string]any)
	requirePlatUsageKeys(t, a, "id", "tenantId", "tenantName", "ruleKey", "metricKey", "severity", "message",
		"firstSeenAt", "lastSeenAt", "status", "snoozedUntil", "detail")
	assert.Equal(t, "", a["detail"], "detail is always present, empty when unset")
	assert.Nil(t, a["snoozedUntil"])
}

func TestPlatformUsage_DetailShape(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	rec := httptest.NewRecorder()
	ops.TenantDetail(rec, platUsageReq("GET", "/x?period=7d", "", "tenantId", platUsageTenantID))
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodePlatUsage(t, rec)
	requirePlatUsageKeys(t, body, "success", "generatedAt", "period", "tenant", "metrics", "series", "topTables",
		"topTablesMeasuredAt", "activity", "alerts")
	requirePlatUsageKeys(t, body["tenant"].(map[string]any), platUsageRowKeys...)
	metrics := body["metrics"].(map[string]any)
	requirePlatUsageKeys(t, metrics, "computeRequestHours", "computeJobHours", "dbShareOfFleetPct", "r2Objects",
		"aiTokensBySource", "dauMauPct", "lastSignInAt", "requests", "requestsPrev", "p95LatencyMs",
		"failedJobs", "deadJobs")
	assert.Nil(t, metrics["r2Objects"])
	assert.Nil(t, metrics["lastSignInAt"])
	assert.InDelta(t, 100.0, metrics["dbShareOfFleetPct"], 1e-9)
	src := metrics["aiTokensBySource"].([]any)[0].(map[string]any)
	requirePlatUsageKeys(t, src, "source", "totalTokens")
	series := body["series"].([]any)
	require.Len(t, series, 7)
	requirePlatUsageKeys(t, series[0].(map[string]any), "date", "computeRequestHours", "computeJobHours", "dbBytes", "r2Bytes")
	requirePlatUsageKeys(t, body["topTables"].([]any)[0].(map[string]any), "tableName", "totalBytes", "rowEstimate")
	assert.Nil(t, body["topTablesMeasuredAt"])
	activity := body["activity"].(map[string]any)
	requirePlatUsageKeys(t, activity, "recordsByModule", "emailsSent", "automationRuns")
	mod := activity["recordsByModule"].([]any)[0].(map[string]any)
	assert.Equal(t, "Sales Orders", mod["label"])
	assert.InDelta(t, 4.0, mod["count"], 1e-9)
	assert.Len(t, body["alerts"], 1)
}

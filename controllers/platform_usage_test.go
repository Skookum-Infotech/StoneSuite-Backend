package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/usage"
)

const (
	platUsageTenantID = "11111111-1111-4111-8111-111111111111"
	platUsageAlertID  = "22222222-2222-4222-8222-222222222222"
)

var platUsageNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type fakePlatUsageTenants struct {
	admin   bool
	tenants []tenancy.Tenant
}

func (f *fakePlatUsageTenants) IsPlatformAdmin(context.Context, string) (bool, error) {
	return f.admin, nil
}
func (f *fakePlatUsageTenants) ListTenants(context.Context) ([]tenancy.Tenant, error) {
	return f.tenants, nil
}
func (f *fakePlatUsageTenants) ListTenantsPage(_ context.Context, _ int, cursor string) ([]tenancy.Tenant, string, error) {
	if cursor == "bad" {
		return nil, "", tenancy.ErrInvalidTenantCursor
	}
	return f.tenants, "next-cursor", nil
}
func (f *fakePlatUsageTenants) TenantByID(_ context.Context, id string) (*tenancy.Tenant, error) {
	for i := range f.tenants {
		if f.tenants[i].ID == id {
			return &f.tenants[i], nil
		}
	}
	return nil, tenancy.ErrTenantNotFound
}

type fakePlatUsageStore struct {
	ackFound   bool
	ackActor   string
	ackSnooze  int
	ackCalls   int
	latestNone bool
}

func (f *fakePlatUsageStore) LatestRolledUpDay(context.Context) (*time.Time, error) {
	if f.latestNone {
		return nil, nil
	}
	d := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	return &d, nil
}

func (f *fakePlatUsageStore) DailyValues(context.Context, []string, time.Time, time.Time) ([]usage.DailyValue, error) {
	d := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	return []usage.DailyValue{
		{TenantID: platUsageTenantID, Day: d, Key: usage.MetricComputeRequestHours, Value: 2},
		{TenantID: platUsageTenantID, Day: d, Key: usage.MetricDBBytes, Value: 1000},
		{TenantID: platUsageTenantID, Day: d, Key: usage.RecordsCreatedKey("sales_order"), Value: 4},
	}, nil
}
func (f *fakePlatUsageStore) TokensByDay(context.Context, []string, time.Time, time.Time) ([]usage.DailyValue, error) {
	return nil, nil
}
func (f *fakePlatUsageStore) TokensBySource(context.Context, string, time.Time, time.Time) (map[string]int64, error) {
	return map[string]int64{"ask": 10}, nil
}
func (f *fakePlatUsageStore) TableSizes(context.Context, string) ([]usage.TableSize, *time.Time, error) {
	return []usage.TableSize{{TableName: "records", TotalBytes: 5, RowEstimate: 2}}, nil, nil
}
func (f *fakePlatUsageStore) SignInStats(context.Context, string, time.Time, time.Time) (usage.SignInStats, error) {
	return usage.SignInStats{ActiveUsers: 1, TotalUsers: 3}, nil
}
func (f *fakePlatUsageStore) Watchlist(context.Context, time.Time) ([]usage.Alert, error) {
	return []usage.Alert{platUsageAlert()}, nil
}
func (f *fakePlatUsageStore) TenantAlerts(context.Context, string, time.Time) ([]usage.Alert, error) {
	return []usage.Alert{platUsageAlert()}, nil
}
func (f *fakePlatUsageStore) TopSeverityByTenant(context.Context, time.Time) (map[string]string, error) {
	return map[string]string{platUsageTenantID: usage.SeverityCritical}, nil
}
func (f *fakePlatUsageStore) AckAlert(_ context.Context, _, actor string, snooze int, _ time.Time) (bool, error) {
	f.ackCalls++
	f.ackActor, f.ackSnooze = actor, snooze
	return f.ackFound, nil
}

func platUsageAlert() usage.Alert {
	return usage.Alert{
		ID: platUsageAlertID, TenantID: platUsageTenantID, TenantName: "Acme", RuleKey: usage.RuleSpike,
		MetricKey: usage.CardCompute, Severity: usage.SeverityCritical, Status: usage.AlertStatusOpen,
		Message: "Compute spiked", FirstSeenAt: platUsageNow, LastSeenAt: platUsageNow,
	}
}

func newPlatUsageOps(admin bool) (*PlatformUsageOps, *fakePlatUsageStore) {
	cp := &fakePlatUsageTenants{admin: admin, tenants: []tenancy.Tenant{
		{ID: platUsageTenantID, Slug: "acme", DisplayName: "Acme", Status: tenancy.StatusActive},
	}}
	st := &fakePlatUsageStore{ackFound: true}
	return NewPlatformUsageOps(cp, st, func() time.Time { return platUsageNow }), st
}

func platUsageReq(method, target, body, pathKey, pathVal string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if pathKey != "" {
		req.SetPathValue(pathKey, pathVal)
	}
	ctx := context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: "identity-1", Email: "admin@example.com"})
	return req.WithContext(ctx)
}

func decodePlatUsage(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func requirePlatUsageKeys(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		assert.Contains(t, m, k)
	}
}

var platUsageRowKeys = []string{"tenantId", "slug", "displayName", "status", "activeUsers", "totalUsers",
	"computeHours", "computeHoursPrev", "dbBytes", "dbBytesPrev", "r2Bytes", "r2BytesPrev",
	"aiTokens", "aiTokensPrev", "errorRatePct", "errorRate24hPct", "computeTrend", "topSeverity"}

func TestPlatformUsage_NonAdminForbidden(t *testing.T) {
	ops, _ := newPlatUsageOps(false)
	tests := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		req  *http.Request
	}{
		{"overview", ops.Overview, platUsageReq("GET", "/api/platform/usage/overview", "", "", "")},
		{"watchlist", ops.Watchlist, platUsageReq("GET", "/api/platform/usage/watchlist", "", "", "")},
		{"detail", ops.TenantDetail, platUsageReq("GET", "/x", "", "tenantId", platUsageTenantID)},
		{"ack", ops.Ack, platUsageReq("POST", "/x", "{}", "alertId", platUsageAlertID)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, tc.req)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Equal(t, false, decodePlatUsage(t, rec)["success"])
		})
	}
}

func TestPlatformUsage_BadParams(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"bad period", "/api/platform/usage/overview?period=14d", http.StatusBadRequest},
		{"limit zero", "/api/platform/usage/overview?limit=0", http.StatusBadRequest},
		{"limit too big", "/api/platform/usage/overview?limit=201", http.StatusBadRequest},
		{"limit not a number", "/api/platform/usage/overview?limit=abc", http.StatusBadRequest},
		{"bad cursor", "/api/platform/usage/overview?cursor=bad", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ops.Overview(rec, platUsageReq("GET", tc.target, "", "", ""))
			assert.Equal(t, tc.want, rec.Code)
			assert.Equal(t, false, decodePlatUsage(t, rec)["success"])
		})
	}
	rec := httptest.NewRecorder()
	ops.TenantDetail(rec, platUsageReq("GET", "/x?period=nope", "", "tenantId", platUsageTenantID))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPlatformUsage_UnknownTenant404(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	for _, id := range []string{"33333333-3333-4333-8333-333333333333", "not-a-uuid"} {
		rec := httptest.NewRecorder()
		ops.TenantDetail(rec, platUsageReq("GET", "/x", "", "tenantId", id))
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "Usage is not available for this tenant.", decodePlatUsage(t, rec)["message"])
	}
}

func TestPlatformUsage_Ack(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		alertID    string
		found      bool
		want       int
		wantSnooze int
	}{
		{"plain ack", `{}`, platUsageAlertID, true, http.StatusOK, 0},
		{"empty body", ``, platUsageAlertID, true, http.StatusOK, 0},
		{"snooze 7", `{"snoozeDays":7}`, platUsageAlertID, true, http.StatusOK, 7},
		{"snooze 30", `{"snoozeDays":30}`, platUsageAlertID, true, http.StatusOK, 30},
		{"snooze 14", `{"snoozeDays":14}`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"snooze zero", `{"snoozeDays":0}`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"invalid json", `{`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"unknown field", `{"foo":1}`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"unknown alert", `{}`, platUsageAlertID, false, http.StatusNotFound, 0},
		{"malformed alert id", `{}`, "nope", true, http.StatusNotFound, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops, st := newPlatUsageOps(true)
			st.ackFound = tc.found
			rec := httptest.NewRecorder()
			ops.Ack(rec, platUsageReq("POST", "/x", tc.body, "alertId", tc.alertID))
			require.Equal(t, tc.want, rec.Code)
			body := decodePlatUsage(t, rec)
			if tc.want == http.StatusOK {
				assert.Equal(t, true, body["success"])
				assert.Equal(t, "identity-1", st.ackActor)
				assert.Equal(t, tc.wantSnooze, st.ackSnooze)
			} else {
				assert.Equal(t, false, body["success"])
				assert.NotEmpty(t, body["message"])
			}
		})
	}
}

func TestPlatformUsage_OverviewShape(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
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
		"firstSeenAt", "lastSeenAt", "status", "snoozedUntil")
	assert.NotContains(t, a, "detail")
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

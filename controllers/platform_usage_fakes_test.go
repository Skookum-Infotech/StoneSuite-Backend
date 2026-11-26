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

// Shared fakes and helpers for the platform usage handler tests.
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
	// signInBatchCalls / signInBatchIDs record SignInStatsByTenant use.
	signInBatchCalls int
	signInBatchIDs   []string
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
func (f *fakePlatUsageStore) SignInStatsByTenant(_ context.Context, ids []string, _, _ time.Time) (map[string]usage.SignInStats, error) {
	f.signInBatchCalls++
	f.signInBatchIDs = ids
	out := map[string]usage.SignInStats{}
	for _, id := range ids {
		out[id] = usage.SignInStats{ActiveUsers: 1, TotalUsers: 3}
	}
	return out, nil
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

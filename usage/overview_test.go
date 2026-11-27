package usage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ymd(s string) time.Time {
	t, _ := time.Parse(dateLayout, s)
	return t
}

func dv(tenant, key, d string, v float64) DailyValue {
	return DailyValue{TenantID: tenant, Key: key, Day: ymd(d), Value: v}
}

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		in   string
		days int
		ok   bool
	}{
		{"7d", 7, true}, {"30d", 30, true}, {"90d", 90, true},
		{"", 0, false}, {"14d", 0, false}, {"30", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			days, ok := ParsePeriod(tc.in)
			assert.Equal(t, tc.days, days)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestWindowEndAndBounds(t *testing.T) {
	now := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	latest := ymd("2026-11-20")
	assert.Equal(t, ymd("2026-11-20"), WindowEnd(&latest, now))
	assert.Equal(t, ymd("2026-11-25"), WindowEnd(nil, now))

	w := NewWindow(ymd("2026-11-10"), 7)
	assert.Equal(t, ymd("2026-11-04"), w.Start())
	assert.Equal(t, ymd("2026-11-03"), w.Prev().End)
	assert.Equal(t, ymd("2026-10-28"), w.Prev().Start())
	assert.Len(t, w.Dates(), 7)
}

func TestBuildRow(t *testing.T) {
	w := NewWindow(ymd("2026-11-10"), 7)
	x := NewValueIndex([]DailyValue{
		dv("t1", MetricComputeRequestHours, "2026-11-09", 1.5),
		dv("t1", MetricComputeJobHours, "2026-11-09", 0.5),
		dv("t1", MetricComputeRequestHours, "2026-11-10", 2),
		dv("t1", MetricComputeRequestHours, "2026-11-01", 4), // previous window
		dv("t1", MetricRequests, "2026-11-09", 50),
		dv("t1", MetricRequests, "2026-11-10", 50),
		dv("t1", MetricErrors5xx, "2026-11-09", 5),
		dv("t1", MetricErrors5xx, "2026-11-10", 5),
		dv("t1", MetricDBBytes, "2026-11-08", 100),
		dv("t1", MetricDBBytes, "2026-11-10", 300),
		dv("t1", MetricDBBytes, "2026-11-02", 80),
		dv("t1", MetricAITokens, "2026-11-05", 10),
	})
	tests := []struct {
		name  string
		info  TenantInfo
		check func(t *testing.T, r TenantUsageRow)
	}{
		{"measured tenant", TenantInfo{ID: "t1", Slug: "acme", DisplayName: "Acme", Status: "active"}, func(t *testing.T, r TenantUsageRow) {
			require.NotNil(t, r.ComputeHours)
			assert.InDelta(t, 4.0, *r.ComputeHours, 1e-9)
			assert.InDelta(t, 4.0, *r.ComputeHoursPrev, 1e-9)
			assert.InDelta(t, 300.0, *r.DBBytes, 1e-9)
			assert.InDelta(t, 80.0, *r.DBBytesPrev, 1e-9)
			assert.Nil(t, r.R2Bytes)
			assert.InDelta(t, 10.0, *r.AITokens, 1e-9)
			assert.Nil(t, r.AITokensPrev)
			assert.InDelta(t, 10.0, *r.ErrorRatePct, 1e-9)
			assert.InDelta(t, 10.0, *r.ErrorRate24hPct, 1e-9)
			assert.Equal(t, []float64{0, 0, 0, 0, 0, 2, 2}, r.ComputeTrend)
			assert.Nil(t, r.TopSeverity)
		}},
		{"tenant without data", TenantInfo{ID: "t2"}, func(t *testing.T, r TenantUsageRow) {
			assert.Nil(t, r.ComputeHours)
			assert.Nil(t, r.DBBytes)
			assert.Nil(t, r.ErrorRatePct)
			assert.Nil(t, r.ErrorRate24hPct)
			assert.Equal(t, make([]float64, 7), r.ComputeTrend)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, BuildRow(tc.info, x, w, SignInStats{ActiveUsers: 2, TotalUsers: 5}, ""))
		})
	}
	row := BuildRow(TenantInfo{ID: "t1"}, x, w, SignInStats{ActiveUsers: 2, TotalUsers: 5}, SeverityWarning)
	require.NotNil(t, row.TopSeverity)
	assert.Equal(t, SeverityWarning, *row.TopSeverity)
	assert.Equal(t, 2, row.ActiveUsers)
	assert.Equal(t, 5, row.TotalUsers)
}

func TestBuildFleet(t *testing.T) {
	w := NewWindow(ymd("2026-11-10"), 7)
	x := NewValueIndex([]DailyValue{
		dv("a", MetricComputeRequestHours, "2026-11-10", 1),
		dv("b", MetricComputeJobHours, "2026-11-10", 2),
		dv("a", MetricDBBytes, "2026-11-10", 100),
		dv("b", MetricDBBytes, "2026-11-09", 50),
	})
	f := BuildFleet([]TenantInfo{{ID: "a", Status: "active"}, {ID: "b", Status: "suspended"}, {ID: "c", Status: "active"}}, x, w, "active")
	assert.Equal(t, 2, f.ActiveTenants)
	assert.Equal(t, 3, f.TotalTenants)
	assert.InDelta(t, 3.0, f.ComputeHours, 1e-9)
	assert.Nil(t, f.ComputeHoursPrev)
	assert.InDelta(t, 150.0, f.DBBytes, 1e-9)
	assert.Equal(t, []float64{0, 0, 0, 0, 0, 0, 3}, f.ComputeTrend)

	empty := BuildFleet(nil, NewValueIndex(), w, "active")
	assert.Equal(t, 0, empty.TotalTenants)
	assert.Equal(t, make([]float64, 7), empty.ComputeTrend)
}

func TestResourceLabel(t *testing.T) {
	tests := map[string]string{
		"sales_order":    "Sales Orders",
		"quote":          "Quotes",
		"credit_memo":    "Credit Memos",
		"vendor_bills":   "Vendor Bills",
		"company":        "Companies",
		"day":            "Days",
		"tax":            "Taxes",
		"inventory":      "Inventory Items",
		"":               "",
		"purchase_order": "Purchase Orders",
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) { assert.Equal(t, want, ResourceLabel(in)) })
	}
}

func TestBuildDetail(t *testing.T) {
	w := NewWindow(ymd("2026-11-10"), 7)
	x := NewValueIndex([]DailyValue{
		dv("t1", MetricComputeRequestHours, "2026-11-10", 2),
		dv("t1", MetricComputeJobHours, "2026-11-09", 1),
		dv("t1", MetricRequests, "2026-11-10", 100),
		dv("t1", MetricRequests, "2026-11-01", 40),
		dv("t1", MetricP95Ms, "2026-11-08", 120),
		dv("t1", MetricP95Ms, "2026-11-10", 300),
		dv("t1", MetricDBBytes, "2026-11-10", 25),
		dv("t1", MetricR2Objects, "2026-11-10", 9),
		dv("t1", MetricActiveUsers, "2026-11-10", 2),
		dv("t1", MetricActiveUsers, "2026-11-09", 4),
		dv("t1", MetricFailedJobs, "2026-11-10", 3),
		dv("t1", MetricDeadJobs, "2026-11-10", 1),
		dv("t1", MetricEmailsSent, "2026-11-10", 7),
		dv("t1", MetricAutomationRuns, "2026-11-10", 2),
		dv("t1", RecordsCreatedKey("quote"), "2026-11-10", 3),
		dv("t1", RecordsCreatedKey("sales_order"), "2026-11-10", 8),
	})
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	d := BuildDetail(DetailInput{
		Now: now, Period: "7d", Info: TenantInfo{ID: "t1"}, Window: w, Values: x,
		Sign: SignInStats{ActiveUsers: 4, TotalUsers: 6}, Sign30: SignInStats{ActiveUsers: 6},
		FleetDB: 100, Tokens: map[string]int64{"ask": 5, "extract": 9},
	})
	assert.InDelta(t, 2.0, d.Metrics.ComputeRequestHours, 1e-9)
	assert.InDelta(t, 1.0, d.Metrics.ComputeJobHours, 1e-9)
	require.NotNil(t, d.Metrics.DBShareOfFleetPct)
	assert.InDelta(t, 25.0, *d.Metrics.DBShareOfFleetPct, 1e-9)
	require.NotNil(t, d.Metrics.R2Objects)
	assert.Equal(t, int64(9), *d.Metrics.R2Objects)
	require.NotNil(t, d.Metrics.DauMauPct)
	assert.InDelta(t, 50.0, *d.Metrics.DauMauPct, 1e-9)
	assert.Equal(t, int64(100), d.Metrics.Requests)
	require.NotNil(t, d.Metrics.RequestsPrev)
	assert.Equal(t, int64(40), *d.Metrics.RequestsPrev)
	require.NotNil(t, d.Metrics.P95LatencyMs)
	assert.InDelta(t, 300.0, *d.Metrics.P95LatencyMs, 1e-9)
	assert.Equal(t, int64(3), d.Metrics.FailedJobs)
	assert.Equal(t, int64(1), d.Metrics.DeadJobs)
	assert.Equal(t, []TokenSource{{"extract", 9}, {"ask", 5}}, d.Metrics.AITokensBySource)
	assert.Len(t, d.Series, 7)
	assert.Equal(t, "2026-11-10", d.Series[6].Date)
	assert.InDelta(t, 25.0, d.Series[6].DBBytes, 1e-9)
	assert.Equal(t, []ModuleCount{{"Sales Orders", 8}, {"Quotes", 3}}, d.Activity.RecordsByModule)
	assert.Equal(t, int64(7), d.Activity.EmailsSent)
	assert.Equal(t, int64(2), d.Activity.AutomationRuns)
	assert.NotNil(t, d.TopTables)
	assert.NotNil(t, d.Alerts)

	bare := BuildDetail(DetailInput{Now: now, Period: "7d", Info: TenantInfo{ID: "zz"}, Window: w, Values: NewValueIndex()})
	assert.Nil(t, bare.Metrics.DBShareOfFleetPct)
	assert.Nil(t, bare.Metrics.DauMauPct)
	assert.Nil(t, bare.Metrics.RequestsPrev)
	assert.Nil(t, bare.Metrics.P95LatencyMs)
	assert.Empty(t, bare.Activity.RecordsByModule)
	assert.NotNil(t, bare.Activity.RecordsByModule)
	assert.NotNil(t, bare.Metrics.AITokensBySource)
}

func TestSumLastAcross(t *testing.T) {
	x := NewValueIndex([]DailyValue{dv("a", MetricDBBytes, "2026-11-08", 10), dv("a", MetricDBBytes, "2026-11-09", 20), dv("b", MetricDBBytes, "2026-11-10", 5)})
	assert.InDelta(t, 25.0, SumLastAcross(x, []string{"a", "b", "c"}, MetricDBBytes, ymd("2026-11-04"), ymd("2026-11-10")), 1e-9)
}

func TestAlertViewsNeverNil(t *testing.T) {
	assert.NotNil(t, AlertViews(nil))
}

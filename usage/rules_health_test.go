package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rule tests for error rate, failed jobs, dormant tenants, quotas and firing order.
func TestEvaluate_ErrorRate(t *testing.T) {
	tests := []struct {
		name     string
		requests int64
		errors   int64
		fire     bool
	}{
		{"under min requests", 499, 499, false},
		{"at min requests under rate", 500, 25, false},
		{"at min requests just over rate", 500, 26, true},
		{"exactly 5 pct", 1000, 50, false},
		{"just over 5 pct", 1000, 51, true},
		{"zero requests", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(RuleInput{Requests24h: tt.requests, Errors5xx24h: tt.errors})
			if tt.fire {
				require.Len(t, got, 1)
				assert.Equal(t, RuleErrorRate, got[0].RuleKey)
				assert.Equal(t, CardErrorRate, got[0].MetricKey)
				assert.Equal(t, SeverityCritical, got[0].Severity)
			} else {
				assert.Empty(t, got)
			}
		})
	}
	got := Evaluate(RuleInput{Requests24h: 3100, Errors5xx24h: 242})
	require.Len(t, got, 1)
	assert.Equal(t, "Server error rate 7.8% over the last 24 hours", got[0].Message)
	assert.Equal(t, "3,100 requests, 242 failed.", got[0].Detail)
}

func TestEvaluate_FailedJobs(t *testing.T) {
	tests := []struct {
		name   string
		failed int64
		dead   int64
		fire   bool
		msg    string
		detail string
	}{
		{"none", 0, 0, false, "", ""},
		{"two failed", 2, 0, false, "", ""},
		{"three failed", 3, 0, true, "3 failed background jobs in the last 24 hours", ""},
		{"one dead", 0, 1, true, "1 dead background job in the last 24 hours", ""},
		{"two dead", 0, 2, true, "2 dead background jobs in the last 24 hours", ""},
		{"dead and failed", 4, 2, true, "2 dead background jobs in the last 24 hours", "4 failed jobs in the same period."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(RuleInput{FailedJobs24h: tt.failed, DeadJobs24h: tt.dead})
			if !tt.fire {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, RuleFailedJobs, got[0].RuleKey)
			assert.Equal(t, CardJobs, got[0].MetricKey)
			assert.Equal(t, SeverityWarning, got[0].Severity)
			assert.Equal(t, tt.msg, got[0].Message)
			assert.Equal(t, tt.detail, got[0].Detail)
		})
	}
}

func TestEvaluate_Dormant(t *testing.T) {
	tests := []struct {
		name   string
		active bool
		days   *int
		fire   bool
		msg    string
	}{
		{"13 days", true, intPtr(13), false, ""},
		{"14 days", true, intPtr(14), true, "No sign-ins for 14 days"},
		{"21 days", true, intPtr(21), true, "No sign-ins for 21 days"},
		{"never signed in", true, nil, true, "No sign-ins yet"},
		{"inactive tenant never dormant", false, intPtr(90), false, ""},
		{"inactive never signed in", false, nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(RuleInput{Active: tt.active, DaysSinceSignIn: tt.days})
			if !tt.fire {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, RuleDormant, got[0].RuleKey)
			assert.Equal(t, CardUsers, got[0].MetricKey)
			assert.Equal(t, SeverityInfo, got[0].Severity)
			assert.Equal(t, tt.msg, got[0].Message)
		})
	}
}

func TestEvaluate_Quota(t *testing.T) {
	series := func(v float64) map[string][]float64 { return map[string][]float64{MetricRequests: {v}} }
	tests := []struct {
		name   string
		series map[string][]float64
		limits map[string]float64
		rule   string
		sev    string
	}{
		{"no limits", series(1000), nil, "", ""},
		{"unrelated limit", series(1000), map[string]float64{CardDB: 10}, "", ""},
		{"non-positive limit", series(1000), map[string]float64{CardRequests: 0}, "", ""},
		{"just under 80", series(79), map[string]float64{CardRequests: 100}, "", ""},
		{"at 80", series(80), map[string]float64{CardRequests: 100}, RuleQuotaNear, SeverityWarning},
		{"just under 100", series(99), map[string]float64{CardRequests: 100}, RuleQuotaNear, SeverityWarning},
		{"at 100", series(100), map[string]float64{CardRequests: 100}, RuleQuotaExceeded, SeverityCritical},
		{"above 100", series(250), map[string]float64{CardRequests: 100}, RuleQuotaExceeded, SeverityCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(RuleInput{Series: tt.series, Limits: tt.limits})
			if tt.rule == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, tt.rule, got[0].RuleKey)
			assert.Equal(t, tt.sev, got[0].Severity)
			assert.Equal(t, CardRequests, got[0].MetricKey)
		})
	}

	t.Run("storage message uses GB", func(t *testing.T) {
		got := Evaluate(RuleInput{
			Series: map[string][]float64{MetricDBBytes: {9 * testGiB}},
			Limits: map[string]float64{CardDB: 10 * testGiB},
		})
		require.Len(t, got, 1)
		assert.Equal(t, "Database storage at 90% of plan limit (9.0 GB of 10.0 GB)", got[0].Message)
	})
}

func TestEvaluate_DeterministicOrder(t *testing.T) {
	in := RuleInput{
		TenantID: "t",
		Active:   true,
		Series: map[string][]float64{
			MetricRequests:    flat(10, 10, 100),
			MetricR2Bytes:     flat(7, 10*testGiB, 30*testGiB),
			MetricActiveUsers: {2},
		},
		Limits:                 map[string]float64{CardRequests: 100},
		ComputePerActiveUser:   5,
		FleetP95ComputePerUser: 1,
		Requests24h:            1000,
		Errors5xx24h:           100,
		DeadJobs24h:            1,
		DaysSinceSignIn:        intPtr(30),
	}
	want := []string{
		"quota_exceeded/requests", "spike/requests", "rapid_growth/r2", "fleet_outlier/compute",
		"error_rate/error_rate", "failed_jobs/jobs", "dormant/users",
	}
	for i := 0; i < 5; i++ {
		assert.Equal(t, want, ruleKeys(Evaluate(in)))
	}
}

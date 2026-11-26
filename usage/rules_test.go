package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flat returns n copies of v followed by last.
func flat(n int, v, last float64) []float64 {
	s := make([]float64, 0, n+1)
	for i := 0; i < n; i++ {
		s = append(s, v)
	}
	return append(s, last)
}

func intPtr(v int) *int { return &v }

func ruleKeys(fs []AlertFiring) []string {
	keys := make([]string, 0, len(fs))
	for _, f := range fs {
		keys = append(keys, f.RuleKey+"/"+f.MetricKey)
	}
	return keys
}

func TestEvaluate_Spike(t *testing.T) {
	// 28 prior days of 10: mean 10, sigma 0, median 10 -> fires when last > 20.
	tests := []struct {
		name   string
		series []float64
		fire   bool
	}{
		{"below 2x median", flat(28, 10, 19), false},
		{"at 2x median", flat(28, 10, 20), false},
		{"above 2x median", flat(28, 10, 20.5), true},
		{"too short", flat(6, 10, 100), false},
		{"minimum history", flat(7, 10, 100), true},
		{"zero baseline", flat(28, 0, 100), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(RuleInput{TenantID: "t", Series: map[string][]float64{MetricRequests: tt.series}})
			if tt.fire {
				require.Len(t, got, 1)
				assert.Equal(t, RuleSpike, got[0].RuleKey)
				assert.Equal(t, CardRequests, got[0].MetricKey)
				assert.Equal(t, SeverityWarning, got[0].Severity)
				require.NotNil(t, got[0].Baseline)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

func TestEvaluate_SpikeNeedsBothConditions(t *testing.T) {
	// One old outlier inflates sigma: mean ~4.5, sigma ~18.7, so mean+3 sigma ~60.
	// 10 is above 2x the 7-day median (1) but not above mean+3 sigma -> no fire.
	series := append([]float64{100}, flat(26, 1, 10)...)
	assert.Empty(t, Evaluate(RuleInput{Series: map[string][]float64{MetricRequests: series}}))

	// 70 clears both conditions.
	series[len(series)-1] = 70
	got := Evaluate(RuleInput{Series: map[string][]float64{MetricRequests: series}})
	require.Len(t, got, 1)
	assert.Equal(t, RuleSpike, got[0].RuleKey)
}

func TestEvaluate_SpikeComputeSumsRequestAndJobHours(t *testing.T) {
	in := RuleInput{TenantID: "t", Series: map[string][]float64{
		MetricComputeRequestHours: flat(10, 1, 3),
		MetricComputeJobHours:     flat(10, 1, 3),
	}}
	got := Evaluate(in)
	require.Len(t, got, 1)
	assert.Equal(t, CardCompute, got[0].MetricKey)
	assert.InDelta(t, 6.0, got[0].Observed, 1e-9)
	assert.InDelta(t, 2.0, *got[0].Baseline, 1e-9)
	assert.Equal(t, "Compute spiked to 6.0 h in a day (28-day average 2.0 h)", got[0].Message)

	// Each half alone (3 vs 1) is under 2x+; sum 6 vs 2 fires but 3 vs 2 would not.
	in.Series[MetricComputeJobHours] = nil
	in.Series[MetricComputeRequestHours] = flat(10, 1, 1.5)
	assert.Empty(t, Evaluate(in))
}

func TestEvaluate_RapidGrowth(t *testing.T) {
	grow := func(prev, last float64) []float64 {
		s := flat(6, prev, prev) // 7 days all prev
		return append(s, last)   // index len-1-7 == first element
	}
	tests := []struct {
		name string
		prev float64
		last float64
		fire bool
		msg  string
	}{
		{"just under 50 pct", 10 * testGiB, 14.9 * testGiB, false, ""},
		{"at 50 pct", 10 * testGiB, 15 * testGiB, true, "Database storage up 50% in 7 days (10.0 GB → 15.0 GB)"},
		{"above 50 pct but under 1 GiB", 1 * testGiB, 1.9 * testGiB, false, ""},
		{"exactly +1 GiB at 100 pct", 1 * testGiB, 2 * testGiB, true, "Database storage up 100% in 7 days (1.0 GB → 2.0 GB)"},
		{"just under +1 GiB", 1.5 * testGiB, 2.49 * testGiB, false, ""},
		{"zero baseline", 0, 5 * testGiB, false, ""},
		{"large", 132 * testGiB, 410 * testGiB, true, "Database storage up 211% in 7 days (132 GB → 410 GB)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(RuleInput{Series: map[string][]float64{MetricDBBytes: grow(tt.prev, tt.last)}})
			if !tt.fire {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, RuleRapidGrowth, got[0].RuleKey)
			assert.Equal(t, CardDB, got[0].MetricKey)
			assert.Equal(t, tt.msg, got[0].Message)
		})
	}

	t.Run("r2 message and key", func(t *testing.T) {
		got := Evaluate(RuleInput{Series: map[string][]float64{MetricR2Bytes: flat(7, 132*testGiB, 410*testGiB)}})
		require.Len(t, got, 1)
		assert.Equal(t, CardR2, got[0].MetricKey)
		assert.Equal(t, "R2 storage up 211% in 7 days (132 GB → 410 GB)", got[0].Message)
	})
	t.Run("too short series", func(t *testing.T) {
		got := Evaluate(RuleInput{Series: map[string][]float64{MetricR2Bytes: {1 * testGiB, 10 * testGiB}}})
		assert.Empty(t, got)
	})
	t.Run("seven points is too short", func(t *testing.T) {
		got := Evaluate(RuleInput{Series: map[string][]float64{MetricR2Bytes: flat(6, 1*testGiB, 10*testGiB)}})
		assert.Empty(t, got)
	})
}

func TestEvaluate_FleetOutlier(t *testing.T) {
	active := map[string][]float64{MetricActiveUsers: {3}}
	tests := []struct {
		name string
		in   RuleInput
		fire bool
	}{
		{"below", RuleInput{Series: active, ComputePerActiveUser: 1.9, FleetP95ComputePerUser: 2}, false},
		{"equal", RuleInput{Series: active, ComputePerActiveUser: 2, FleetP95ComputePerUser: 2}, false},
		{"above", RuleInput{Series: active, ComputePerActiveUser: 2.1, FleetP95ComputePerUser: 2}, true},
		{"no active users", RuleInput{Series: map[string][]float64{MetricActiveUsers: {0}}, ComputePerActiveUser: 9, FleetP95ComputePerUser: 2}, false},
		{"no users series", RuleInput{ComputePerActiveUser: 9, FleetP95ComputePerUser: 2}, false},
		{"zero fleet p95", RuleInput{Series: active, ComputePerActiveUser: 9, FleetP95ComputePerUser: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.in)
			if !tt.fire {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, RuleFleetOutlier, got[0].RuleKey)
			assert.Equal(t, CardCompute, got[0].MetricKey)
			assert.Equal(t, "Compute per active user is above the fleet's 95th percentile (2.1 h vs 2.0 h)", got[0].Message)
		})
	}
}

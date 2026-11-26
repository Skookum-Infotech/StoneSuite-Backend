package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

// Tests for the rollup's rule pass (quiet-alert resolution, fleet p95) and its pure helpers.
func TestRollup_ResolveQuietUsesDayMinusTwo(t *testing.T) {
	store := newRollupFakeStore()
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "")}, rollupFakePools{}, store, nil, rollupFakeSource{})

	_, err := r.Run(context.Background(), rollupDay.Add(13*time.Hour))
	require.NoError(t, err)
	require.Len(t, store.quiet, 1)
	assert.Equal(t, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), store.quiet[0].quietSince)
}

func TestRollup_FleetP95FeedsOutlierRule(t *testing.T) {
	store := newRollupFakeStore()
	var tenants []tenancy.Tenant
	ids := []string{"t01", "t02", "t03", "t04", "t05", "t06", "t07", "t08", "t09", "t10",
		"t11", "t12", "t13", "t14", "t15", "t16", "t17", "t18", "t19", "t20"}
	for i, id := range ids {
		tenants = append(tenants, rollupTenant(id, "s"+id, ""))
		store.signIn[id] = SignInStats{ActiveUsers: 1, LastSignInAt: &rollupDay}
		store.hourly[id] = HourlyTotals{HandlerMs: int64(i+1) * 3_600_000}
	}
	r := newTestRollup(tenants, rollupFakePools{}, store, nil, rollupFakeSource{})

	_, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	var outliers []string
	for _, f := range store.firings {
		if f.RuleKey == RuleFleetOutlier {
			outliers = append(outliers, f.TenantID)
		}
	}
	assert.Equal(t, []string{"t20"}, outliers)
}

func TestComputePerUser(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]float64
		active int
		want   float64
	}{
		{"no active users", map[string]float64{MetricComputeRequestHours: 4}, 0, 0},
		{"requests and jobs", map[string]float64{MetricComputeRequestHours: 3, MetricComputeJobHours: 1}, 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, computePerUser(tt.values, tt.active))
		})
	}
}

func TestDaysSince(t *testing.T) {
	ref := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	assert.Nil(t, daysSince(nil, ref))
	old := ref.AddDate(0, 0, -15)
	require.NotNil(t, daysSince(&old, ref))
	assert.Equal(t, 15, *daysSince(&old, ref))
	future := ref.Add(time.Hour)
	assert.Equal(t, 0, *daysSince(&future, ref))
}

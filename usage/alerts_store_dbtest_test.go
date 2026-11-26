//go:build dbtest

package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func firing(tenant, rule, metric, severity string, observed float64) AlertFiring {
	return AlertFiring{TenantID: tenant, RuleKey: rule, MetricKey: metric, Severity: severity,
		Message: "m", Detail: "d", Observed: observed}
}

func TestFleetStore_RecordFiringOneLiveAlertPerRuleMetric(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant := insertTenant(t, pool)
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)

	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "spike", MetricRequests, SeverityInfo, 1), now))
	base := 5.0
	f := firing(tenant, "spike", MetricRequests, SeverityCritical, 9)
	f.Baseline = &base
	require.NoError(t, s.RecordFiring(ctx, f, now.Add(time.Hour)))

	alerts, err := s.TenantAlerts(ctx, tenant, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, alerts, 1, "second firing merges into the live alert")
	assert.Equal(t, SeverityCritical, alerts[0].Severity)
	assert.Equal(t, AlertStatusOpen, alerts[0].Status)
	require.NotNil(t, alerts[0].Observed)
	assert.Equal(t, 9.0, *alerts[0].Observed)
	require.NotNil(t, alerts[0].Baseline)
	assert.True(t, alerts[0].FirstSeenAt.Equal(now))
	assert.True(t, alerts[0].LastSeenAt.Equal(now.Add(time.Hour)))
	assert.Equal(t, "Usage Test", alerts[0].TenantName)

	// A different metric opens its own alert.
	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "spike", MetricErrors5xx, SeverityWarning, 2), now))
	alerts, err = s.TenantAlerts(ctx, tenant, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Len(t, alerts, 2)
}

func TestFleetStore_AckSnoozeExpiryAndWatchlist(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant := insertTenant(t, pool)
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)

	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "a", MetricRequests, SeverityInfo, 1), now))
	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "b", MetricErrors5xx, SeverityCritical, 1), now))
	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "c", MetricP95Ms, SeverityWarning, 1), now))

	watch, err := s.Watchlist(ctx, now)
	require.NoError(t, err)
	var mine []Alert
	for _, a := range watch {
		if a.TenantID == tenant {
			mine = append(mine, a)
		}
	}
	require.Len(t, mine, 3)
	assert.Equal(t, []string{SeverityCritical, SeverityWarning, SeverityInfo},
		[]string{mine[0].Severity, mine[1].Severity, mine[2].Severity})

	top, err := s.TopSeverityByTenant(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, SeverityCritical, top[tenant])

	actor := insertIdentity(t, ctx, s, tenant)
	ok, err := s.AckAlert(ctx, mine[0].ID, actor, 0, now)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = s.AckAlert(ctx, mine[2].ID, actor, 7, now)
	require.NoError(t, err)
	assert.True(t, ok)
	_, err = s.AckAlert(ctx, mine[1].ID, actor, 3, now)
	assert.Error(t, err, "unsupported snooze length")

	top, err = s.TopSeverityByTenant(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, SeverityWarning, top[tenant], "acked and snoozed alerts leave the watch-list")

	// Snooze expiry: alert reappears and a new firing flips it back to open.
	later := now.AddDate(0, 0, 8)
	// The warning alert is still open, so it stays the top severity; the
	// expired info snooze is back on the watch list alongside it.
	top, err = s.TopSeverityByTenant(ctx, later)
	require.NoError(t, err)
	assert.Equal(t, SeverityWarning, top[tenant])
	watch, err = s.Watchlist(ctx, later)
	require.NoError(t, err)
	var back bool
	for _, a := range watch {
		if a.ID == mine[2].ID {
			back = true
		}
	}
	assert.True(t, back, "an expired snooze returns to the watch list")
	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "a", MetricRequests, SeverityInfo, 2), later))
	alerts, err := s.TenantAlerts(ctx, tenant, now.Add(-time.Hour))
	require.NoError(t, err)
	for _, a := range alerts {
		if a.RuleKey == "a" {
			assert.Equal(t, AlertStatusOpen, a.Status)
			assert.Nil(t, a.SnoozedUntil)
		}
	}

	ok, err = s.AckAlert(ctx, "00000000-0000-0000-0000-000000000000", actor, 0, now)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestFleetStore_ResolveQuiet(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	s := NewFleetStore(pool)
	tenant, other := insertTenant(t, pool), insertTenant(t, pool)
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)

	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "old", MetricRequests, SeverityInfo, 1), now.AddDate(0, 0, -5)))
	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "fresh", MetricRequests, SeverityInfo, 1), now))
	require.NoError(t, s.RecordFiring(ctx, firing(other, "old", MetricRequests, SeverityInfo, 1), now.AddDate(0, 0, -5)))

	n, err := s.ResolveQuiet(ctx, tenant, now.AddDate(0, 0, -2), now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	alerts, err := s.TenantAlerts(ctx, tenant, now.AddDate(0, 0, -10))
	require.NoError(t, err)
	for _, a := range alerts {
		if a.RuleKey == "old" {
			assert.Equal(t, AlertStatusResolved, a.Status)
		} else {
			assert.Equal(t, AlertStatusOpen, a.Status)
		}
	}
	otherAlerts, err := s.TenantAlerts(ctx, other, now.AddDate(0, 0, -10))
	require.NoError(t, err)
	require.Len(t, otherAlerts, 1)
	assert.Equal(t, AlertStatusOpen, otherAlerts[0].Status, "other tenant untouched")

	// Once resolved, the same rule+metric may open a fresh alert.
	require.NoError(t, s.RecordFiring(ctx, firing(tenant, "old", MetricRequests, SeverityInfo, 1), now))
	alerts, err = s.TenantAlerts(ctx, tenant, now.AddDate(0, 0, -10))
	require.NoError(t, err)
	assert.Len(t, alerts, 3)
}

// insertIdentity creates an identity row to act as the alert actor.
func insertIdentity(t *testing.T, ctx context.Context, s *FleetStore, tenant string) string {
	t.Helper()
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identities (tenant_id, email, password_hash)
		VALUES ($1, 'actor-' || substr(gen_random_uuid()::text, 1, 8) || '@example.test', 'x')
		RETURNING id::text`, tenant).Scan(&id)
	require.NoError(t, err)
	return id
}

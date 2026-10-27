//go:build dbtest

package automation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

// TestAnomalyScanHonoursTheWorkspaceSwitch proves the workspace-level switch:
// with the anomaly_scan rule off the job neither scans nor announces; with it
// on (the seeded default) it announces the pending finding.
func TestAnomalyScanHonoursTheWorkspaceSwitch(t *testing.T) {
	pool := newTenantTestPool(t)
	ctx := context.Background()

	var seeded bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT enabled FROM automation_rules WHERE rule_key = $1`, RuleKeyAnomalyScan).Scan(&seeded),
		"the anomaly_scan rule must be seeded by schema.sql")
	assert.True(t, seeded, "the anomaly_scan rule ships enabled")
	setRule := func(on bool) {
		_, err := pool.Exec(ctx, `UPDATE automation_rules SET enabled = $2 WHERE rule_key = $1`, RuleKeyAnomalyScan, on)
		require.NoError(t, err)
	}
	t.Cleanup(func() { setRule(true) })

	var anomalyID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO anomaly (anomaly_rule_key, anomaly_subject_key, anomaly_severity, anomaly_resource, anomaly_title)
		VALUES ('dbtest_switch', gen_random_uuid()::text, 'warning', 'expense', 'switch test')
		RETURNING anomaly_id`).Scan(&anomalyID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM anomaly WHERE anomaly_id = $1`, anomalyID) })

	notified := func() bool {
		var at *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT anomaly_notified_at FROM anomaly WHERE anomaly_id = $1`, anomalyID).Scan(&at))
		return at != nil
	}
	now := time.Now()
	env := JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-anomaly-switch"}, Now: now, Today: now, Loc: time.UTC}
	send := &fakeSender{}
	job := &AnomalyScan{deps: Deps{Send: send}}

	var before int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM anomaly`).Scan(&before))

	setRule(false)
	res, err := job.Run(ctx, pool, env)
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
	assert.False(t, notified(), "a switched-off workspace announces nothing")
	assert.Empty(t, send.reqs)
	var after int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM anomaly`).Scan(&after))
	assert.Equal(t, before, after, "a switched-off workspace is not scanned")

	setRule(true)
	_, err = job.Run(ctx, pool, env)
	require.NoError(t, err)
	assert.True(t, notified(), "once switched back on, the pending finding is announced")
}

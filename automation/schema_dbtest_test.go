//go:build dbtest

package automation

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTenantTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestAutomationSchema(t *testing.T) {
	ctx := context.Background()
	pool := newTenantTestPool(t)

	// Leftovers from an aborted run would break re-runs.
	_, err := pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id IN (SELECT id FROM automation_rules WHERE rule_key = 'test.schema')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM automation_rules WHERE rule_key = 'test.schema'`)
	require.NoError(t, err)

	var ruleID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO automation_rules (rule_key, name, audience)
		VALUES ('test.schema', 'Schema test', 'internal')
		ON CONFLICT (rule_key) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`).Scan(&ruleID))
	t.Cleanup(func() {
		_, delErr := pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id = $1`, ruleID)
		require.NoError(t, delErr)
		_, delErr = pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1`, ruleID)
		require.NoError(t, delErr)
	})

	insert := `
		INSERT INTO automation_deliveries
			(rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind, run_at)
		VALUES ($1, 'invoice', '42', '2026-10-20', 0, $2, 'external', now())
		ON CONFLICT (occurrence_key) DO NOTHING`
	tag, err := pool.Exec(ctx, insert, ruleID, ruleID+":invoice:42:2026-10-20:0")
	require.NoError(t, err)
	assert.EqualValues(t, 1, tag.RowsAffected())
	tag, err = pool.Exec(ctx, insert, ruleID, ruleID+":invoice:42:2026-10-20:0")
	require.NoError(t, err)
	assert.EqualValues(t, 0, tag.RowsAffected(), "occurrence_key must dedupe")

	_, err = pool.Exec(ctx, `UPDATE automation_deliveries SET status = 'bogus' WHERE rule_id = $1`, ruleID)
	assert.Error(t, err, "status CHECK must reject unknown states")

	_, err = pool.Exec(ctx, `INSERT INTO automation_rules (rule_key, name, audience) VALUES ('test.bad', 'x', 'everyone')`)
	assert.Error(t, err, "audience CHECK must reject unknown audiences")

	_, err = pool.Exec(ctx, `INSERT INTO automation_suppressions (email, source) VALUES ('Mixed@Case.com', 'manual')`)
	assert.Error(t, err, "suppressed emails must be stored lower-cased")

	var cap int
	require.NoError(t, pool.QueryRow(ctx, `SELECT daily_external_cap FROM automation_settings`).Scan(&cap))
	assert.Equal(t, 200, cap)
}

func TestARDunningSchema(t *testing.T) {
	ctx := context.Background()
	pool := newTenantTestPool(t)

	var (
		count   int
		enabled bool
		name    string
		aud     string
		system  bool
		raw     []byte
	)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM automation_rules WHERE rule_key = $1`, RuleKeyARDunning).Scan(&count))
	assert.Equal(t, 1, count, "ar_dunning must be seeded exactly once")

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT name, audience, is_system, enabled, rule FROM automation_rules WHERE rule_key = $1`,
		RuleKeyARDunning).Scan(&name, &aud, &system, &enabled, &raw))
	assert.Equal(t, "Invoice payment reminders", name)
	assert.Equal(t, "external", aud)
	assert.True(t, system)
	assert.False(t, enabled, "seeded rule must be disabled")

	var cfg ARRuleConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))
	offsets := make([]int, 0, len(cfg.Cadence))
	for _, c := range cfg.Cadence {
		offsets = append(offsets, c.OffsetDays)
	}
	assert.Equal(t, []int{-3, 0, 7, 14}, offsets)

	var stateRows int
	var enabledAtNull bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(bool_and(s.enabled_at IS NULL), TRUE)
		FROM automation_state s JOIN automation_rules r ON r.id = s.rule_id
		WHERE r.rule_key = $1`, RuleKeyARDunning).Scan(&stateRows, &enabledAtNull))
	assert.Equal(t, 1, stateRows)
	assert.True(t, enabledAtNull)

	var colType string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_name = 'automation_deliveries' AND column_name = 'sent_at'`).Scan(&colType))
	assert.Equal(t, "timestamp with time zone", colType)

	var indexDef string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE indexname = 'idx_automation_deliveries_contact_sent'`).Scan(&indexDef))
	assert.Contains(t, indexDef, "recipient_email, sent_at")
	assert.Contains(t, indexDef, "'sent'")
}

func TestExpiryRulesSchema(t *testing.T) {
	ctx := context.Background()
	pool := newTenantTestPool(t)
	for key, name := range map[string]string{
		RuleKeyQuoteExpiry:    "Quote expiry reminders",
		RuleKeyEstimateExpiry: "Estimate expiry reminders",
	} {
		var (
			gotName, aud string
			system, on   bool
			raw          []byte
			states       int
		)
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT name, audience, is_system, enabled, rule FROM automation_rules WHERE rule_key = $1`, key).
			Scan(&gotName, &aud, &system, &on, &raw), key)
		assert.Equal(t, name, gotName)
		assert.Equal(t, "internal", aud)
		assert.True(t, system)
		assert.False(t, on, "seeded rule must be disabled")
		var cfg ARRuleConfig
		require.NoError(t, json.Unmarshal(raw, &cfg))
		require.Len(t, cfg.Cadence, 1)
		assert.Equal(t, -3, cfg.Cadence[0].OffsetDays)
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM automation_state s JOIN automation_rules r ON r.id = s.rule_id
			WHERE r.rule_key = $1`, key).Scan(&states))
		assert.Equal(t, 1, states)
	}
}

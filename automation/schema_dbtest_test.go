//go:build dbtest

package automation

import (
	"context"
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

	var ruleID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO automation_rules (rule_key, name, audience)
		VALUES ('test.schema', 'Schema test', 'internal')
		ON CONFLICT (rule_key) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`).Scan(&ruleID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1`, ruleID) })

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

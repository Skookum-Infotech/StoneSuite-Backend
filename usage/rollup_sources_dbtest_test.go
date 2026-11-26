//go:build dbtest

package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollupTenantPool connects to a tenant database already built from tenant/schema.sql.
func rollupTenantPool(t *testing.T) *pgxpool.Pool {
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

func TestPGTenantSource_SizesAndTables(t *testing.T) {
	ctx := context.Background()
	src := newPGTenantSource(rollupTenantPool(t))

	size, err := src.DBSize(ctx)
	require.NoError(t, err)
	assert.Positive(t, size)

	tables, err := src.TopTables(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tables)
	assert.LessOrEqual(t, len(tables), topTableLimit)
	for i := 1; i < len(tables); i++ {
		assert.GreaterOrEqual(t, tables[i-1].TotalBytes, tables[i].TotalBytes)
	}
	for _, tb := range tables {
		assert.GreaterOrEqual(t, tb.RowEstimate, int64(0))
	}
}

func TestPGTenantSource_AuditAndDeliveries(t *testing.T) {
	ctx := context.Background()
	pool := rollupTenantPool(t)
	src := newPGTenantSource(pool)

	day := time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC)
	inDay, outDay := day.Add(time.Hour), day.AddDate(0, 0, 1)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_logs WHERE resource LIKE 'rollup_test_%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE resource = 'rollup_test'`)
		_, _ = pool.Exec(ctx, `DELETE FROM automation_rules WHERE rule_key = 'rollup_test_rule'`)
	})
	for _, a := range []struct {
		action, resource string
		at               time.Time
	}{
		{"create", "rollup_test_a", inDay}, {"create", "rollup_test_a", inDay}, {"create", "rollup_test_b", inDay},
		{"create", "rollup_test_a", outDay}, {"update", "rollup_test_a", inDay},
		{"document.sent", "rollup_test_a", inDay},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO audit_logs (action, resource, created_at) VALUES ($1, $2, $3)`,
			a.action, a.resource, a.at)
		require.NoError(t, err)
	}
	created, err := src.RecordsCreated(ctx, day, outDay)
	require.NoError(t, err)
	assert.Equal(t, int64(2), created["rollup_test_a"])
	assert.Equal(t, int64(1), created["rollup_test_b"])

	sent, err := src.DocumentsSent(ctx, day, outDay)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, sent, int64(1))

	var ruleID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO automation_rules (rule_key, name, audience) VALUES ('rollup_test_rule', 'Rollup test', 'internal')
		RETURNING id::text`).Scan(&ruleID))
	for i, st := range []string{"sent", "sent", "failed", "planned", "claimed"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO automation_deliveries
			    (rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind, run_at, status)
			VALUES ($1, 'rollup_test', 'r', $2, $3, $4, 'internal', $5, $6)`,
			ruleID, day, i, "rollup_test_"+st+string(rune('a'+i)), inDay, st)
		require.NoError(t, err)
	}
	runs, delivered, err := src.Deliveries(ctx, day, outDay)
	require.NoError(t, err)
	assert.Equal(t, int64(3), runs)
	assert.Equal(t, int64(2), delivered)
}

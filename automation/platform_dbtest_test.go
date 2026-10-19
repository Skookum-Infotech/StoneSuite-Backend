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

// newCPTestPool connects to TEST_CP_DATABASE_URL (skipping when unset) and
// ensures the platform_automation_settings table exists, by applying only
// that table's DDL (the full control-plane schema needs pgvector and more).
func newCPTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS platform_automation_settings (
			id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			enabled BOOLEAN NOT NULL DEFAULT FALSE,
			updated_by TEXT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		DELETE FROM platform_automation_settings`)
	require.NoError(t, err)
	return pool
}

func TestPlatformStore(t *testing.T) {
	ctx := context.Background()
	s := NewPlatformStore(newCPTestPool(t))

	st, err := s.State(ctx) // no row: disabled, never an error
	require.NoError(t, err)
	assert.False(t, st.Enabled)
	assert.Nil(t, st.UpdatedAt)

	require.NoError(t, s.Set(ctx, true, "admin@example.com"))
	on, err := s.Enabled(ctx)
	require.NoError(t, err)
	assert.True(t, on)
	st, _ = s.State(ctx)
	assert.Equal(t, "admin@example.com", st.UpdatedBy)
	assert.NotNil(t, st.UpdatedAt)

	require.NoError(t, s.Set(ctx, false, "admin2@example.com"))
	on, _ = s.Enabled(ctx)
	assert.False(t, on)
}

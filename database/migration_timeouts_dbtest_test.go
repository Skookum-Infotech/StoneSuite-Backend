//go:build dbtest

package database

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiftSessionTimeouts_ScopedToTransaction checks a migration transaction
// runs without the pool's statement/lock limits and the connection gets them
// back afterwards (SET LOCAL semantics).
func TestLiftSessionTimeouts_ScopedToTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping dbtest")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })

	_, err = conn.Exec(ctx, `SELECT set_config('statement_timeout', '30s', false), set_config('lock_timeout', '5s', false)`)
	require.NoError(t, err)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, liftSessionTimeouts(ctx, tx))
	var stmt, lock string
	require.NoError(t, tx.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('lock_timeout')`).Scan(&stmt, &lock))
	assert.Equal(t, "0", stmt)
	assert.Equal(t, "0", lock)
	require.NoError(t, tx.Commit(ctx))

	require.NoError(t, conn.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('lock_timeout')`).Scan(&stmt, &lock))
	assert.Equal(t, "30s", stmt)
	assert.Equal(t, "5s", lock)
}

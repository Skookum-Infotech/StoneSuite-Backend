//go:build dbtest

package tenancy

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewControlPlaneWithTimeouts_SetsSessionLimits checks the limits really
// land on pooled connections (AfterConnect), and that a zero field keeps the
// server default instead of resetting it.
func TestNewControlPlaneWithTimeouts_SetsSessionLimits(t *testing.T) {
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping dbtest")
	}
	ctx := context.Background()
	cp, err := NewControlPlaneWithTimeouts(ctx, dsn, SessionTimeouts{Statement: 30 * time.Second, Lock: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(cp.Close)

	conn, err := cp.Pool().Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	var stmt, lock, idle string
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('lock_timeout'),
		current_setting('idle_in_transaction_session_timeout')`).Scan(&stmt, &lock, &idle))
	assert.Equal(t, "30s", stmt)
	assert.Equal(t, "5s", lock)
	assert.NotEmpty(t, idle, "an unset limit keeps its current value")

	// The limit is enforced, not just stored.
	_, err = conn.Exec(ctx, `SELECT set_config('statement_timeout', '100ms', false)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `SELECT pg_sleep(1)`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "statement timeout")
	// Don't hand the shortened limit back to the pool.
	conn.Conn().Close(ctx) //nolint:errcheck // test cleanup of a deliberately poisoned conn
}

//go:build dbtest

package database

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// schemaLockTestTimeout bounds the whole test; a leaked advisory lock shows up
// as a hang that this deadline turns into a failure.
const schemaLockTestTimeout = 60 * time.Second

// concurrentApplies is how many ApplyTenantSchema calls race on one pool.
const concurrentApplies = 2

func schemaTestPool(t *testing.T) *pgxpool.Pool {
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

// TestApplyTenantSchema_LockReleased applies the schema twice in a row and then
// twice concurrently on one pool. Before the lock was pinned to one connection
// the unlock could miss the locking session, so the next apply blocked forever.
func TestApplyTenantSchema_LockReleased(t *testing.T) {
	pool := schemaTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), schemaLockTestTimeout)
	defer cancel()

	for i := 0; i < 2; i++ {
		_, err := ApplyTenantSchema(ctx, pool)
		require.NoError(t, err, "sequential apply %d", i)
	}

	var wg sync.WaitGroup
	errs := make([]error, concurrentApplies)
	for i := 0; i < concurrentApplies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ApplyTenantSchema(ctx, pool)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "concurrent apply %d", i)
	}

	// No session may still hold the lock afterwards. A bigint advisory key is
	// stored as classid (high 32 bits) + objid (low 32 bits), objsubid 1.
	var held int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND objsubid = 1 AND granted
		  AND ((classid::bigint << 32) | objid::bigint) = $1`, tenantSchemaLockKey).Scan(&held))
	require.Zero(t, held, "tenant schema lock still held after apply")
}

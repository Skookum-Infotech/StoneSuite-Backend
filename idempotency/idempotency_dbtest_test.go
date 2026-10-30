//go:build dbtest

package idempotency_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func testPool(t *testing.T) *pgxpool.Pool {
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

func TestClaim_ConcurrentSameKeyOneWinner(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	scope := "test.claim"
	key := fmt.Sprintf("k-%d", time.Now().UnixNano())
	hash := idempotency.HashRequest([]byte("body"))

	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	fresh := 0
	errs := []error{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			prior, err := idempotency.Claim(ctx, tx, scope, key, hash)
			if err == nil && prior == "" {
				err = idempotency.Complete(ctx, tx, scope, key, "ref-1")
				if err == nil {
					err = tx.Commit(ctx)
				}
				mu.Lock()
				if err == nil {
					fresh++
				}
				mu.Unlock()
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Empty(t, errs)
	assert.Equal(t, 1, fresh)

	ref, err := idempotency.Lookup(ctx, pool, scope, key, hash)
	require.NoError(t, err)
	assert.Equal(t, "ref-1", ref)
	_, err = idempotency.Lookup(ctx, pool, scope, key, idempotency.HashRequest([]byte("other")))
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

//go:build dbtest

package domainevent

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/provenance"
)

const (
	testResource = "domainevent_test"
	testPrefix   = "dbtest."
)

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM domain_event WHERE resource = $1`, testResource)
		_, _ = pool.Exec(context.Background(), `DELETE FROM domain_event_cursor WHERE consumer LIKE $1`, testPrefix+"%")
	}
	cleanup()
	t.Cleanup(cleanup)
	return pool
}

func emitN(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, Emit(context.Background(), pool, Event{Type: "t", Resource: testResource, RecordID: "r"}))
	}
}

func count(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM domain_event WHERE resource = $1`, testResource).Scan(&n))
	return n
}

func cursor(t *testing.T, pool *pgxpool.Pool, c string) (last int64, failing *int64, failures int) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT last_id, failing_id, failures FROM domain_event_cursor WHERE consumer = $1`, c).Scan(&last, &failing, &failures))
	return
}

func regWith(t *testing.T, name string, h Handler) *Registry {
	t.Helper()
	r := NewRegistry()
	require.NoError(t, r.Register(name, h))
	return r
}

func TestEmitAtomicWithTx(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, Emit(ctx, tx, Event{Type: "t", Resource: testResource, RecordID: "1"}))
	require.NoError(t, tx.Rollback(ctx))
	assert.Equal(t, 0, count(t, pool))

	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, Emit(ctx, tx, Event{Type: "t", Resource: testResource, RecordID: "1"}))
	require.NoError(t, tx.Commit(ctx))
	assert.Equal(t, 1, count(t, pool))
}

func TestEmitViaFromContext(t *testing.T) {
	ctx := provenance.With(context.Background(), provenance.Automation)
	pool := newPool(t)
	require.NoError(t, Emit(ctx, pool, Event{Type: "t", Resource: testResource, RecordID: "1"}))
	require.NoError(t, Emit(ctx, pool, Event{Type: "t", Resource: testResource, RecordID: "2", Via: provenance.AIAgent}))
	rows, err := pool.Query(ctx, `SELECT via FROM domain_event WHERE resource = $1 ORDER BY id`, testResource)
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		got = append(got, v)
	}
	assert.Equal(t, []string{"automation", "ai_agent"}, got)
}

func TestDrainOrderAndCursor(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	c := testPrefix + "order"
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, Emit(ctx, pool, Event{Type: "t", Resource: testResource, RecordID: id}))
	}
	var seen []string
	reg := regWith(t, c, func(_ context.Context, _ pgx.Tx, ev Stored) error {
		seen = append(seen, ev.RecordID)
		return nil
	})
	res, err := Drain(ctx, pool, reg, c, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Handled)
	res, err = Drain(ctx, pool, reg, c, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Handled)
	assert.Equal(t, []string{"a", "b", "c"}, seen)
	res, err = Drain(ctx, pool, reg, c, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Handled)
}

func TestDrainConcurrentSingleDrainer(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	c := testPrefix + "conc"
	emitN(t, pool, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	reg := regWith(t, c, func(context.Context, pgx.Tx, Stored) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	done := make(chan DrainResult, 1)
	go func() {
		r, _ := Drain(ctx, pool, reg, c, 10)
		done <- r
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first drainer never started")
	}
	second, err := Drain(ctx, pool, reg, c, 10)
	require.NoError(t, err)
	assert.True(t, second.Busy)
	close(release)
	first := <-done
	assert.False(t, first.Busy)
	assert.Equal(t, 1, first.Handled)
}

func TestDrainFailureRetryAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	c := testPrefix + "fail"
	emitN(t, pool, 2)
	var poisonID atomic.Int64
	var handled []int64
	reg := regWith(t, c, func(_ context.Context, _ pgx.Tx, ev Stored) error {
		if poisonID.CompareAndSwap(0, ev.ID) || poisonID.Load() == ev.ID {
			return errors.New("boom")
		}
		handled = append(handled, ev.ID)
		return nil
	})
	for i := 1; i < MaxAttempts; i++ {
		res, err := Drain(ctx, pool, reg, c, 10)
		require.NoError(t, err)
		assert.True(t, res.Failed)
		assert.Zero(t, res.Handled)
		last, failing, failures := cursor(t, pool, c)
		assert.Zero(t, last, "cursor must not advance")
		require.NotNil(t, failing)
		assert.Equal(t, poisonID.Load(), *failing)
		assert.Equal(t, i, failures)
	}
	res, err := Drain(ctx, pool, reg, c, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Skipped)
	assert.Equal(t, 1, res.Handled)
	assert.False(t, res.Failed)
	assert.Len(t, handled, 1)
	last, failing, failures := cursor(t, pool, c)
	assert.Equal(t, handled[0], last)
	assert.Nil(t, failing)
	assert.Zero(t, failures)
}

func TestDrainPanicIsFailure(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	c := testPrefix + "panic"
	emitN(t, pool, 1)
	reg := regWith(t, c, func(context.Context, pgx.Tx, Stored) error { panic("kaboom") })
	res, err := Drain(ctx, pool, reg, c, 10)
	require.NoError(t, err)
	assert.True(t, res.Failed)
	_, _, failures := cursor(t, pool, c)
	assert.Equal(t, 1, failures)
}

func TestDrainAllIsolatesFailingConsumer(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	bad, good := testPrefix+"a_bad", testPrefix+"b_good"
	emitN(t, pool, 2)
	reg := NewRegistry()
	require.NoError(t, reg.Register(bad, func(context.Context, pgx.Tx, Stored) error { return errors.New("boom") }))
	var n int
	require.NoError(t, reg.Register(good, func(context.Context, pgx.Tx, Stored) error { n++; return nil }))
	res, err := DrainAll(ctx, pool, reg, 0)
	require.NoError(t, err)
	assert.True(t, res.Failed)
	assert.Equal(t, 2, n)
	assert.Equal(t, 2, res.Handled)
	last, _, _ := cursor(t, pool, bad)
	assert.Zero(t, last)
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	emitN(t, pool, 3)
	_, err := pool.Exec(ctx, `UPDATE domain_event SET created_at = now() - interval '40 days' WHERE resource = $1`, testResource)
	require.NoError(t, err)
	var ids []int64
	rows, err := pool.Query(ctx, `SELECT id FROM domain_event WHERE resource = $1 ORDER BY id`, testResource)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	// A recent event must survive regardless of the cursor.
	emitN(t, pool, 1)
	_, err = pool.Exec(ctx, `INSERT INTO domain_event_cursor (consumer, last_id) VALUES ($1, $2)`, testPrefix+"prune", ids[1])
	require.NoError(t, err)
	// Other tenants' real cursors may exist; only assert on our rows.
	_, err = Prune(ctx, pool, 30)
	require.NoError(t, err)
	var remaining []int64
	rows, err = pool.Query(ctx, `SELECT id FROM domain_event WHERE resource = $1 ORDER BY id`, testResource)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		remaining = append(remaining, id)
	}
	rows.Close()
	assert.NotContains(t, remaining, ids[0], "old and below cursor is pruned")
	assert.Contains(t, remaining, ids[2], "old but beyond cursor is kept")
	assert.NotContains(t, remaining, ids[1], "old and at the cursor is pruned")
	assert.Len(t, remaining, 2, "ids[2] and the recent event are kept")
}

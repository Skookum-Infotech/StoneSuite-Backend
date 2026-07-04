//go:build dbtest

package index

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestPool connects to a disposable Postgres (with the rag_index_queue
// table already migrated) named by TEST_DATABASE_URL. Skips if unset, so
// `go test ./...` (no dbtest tag, no env var) never touches the network.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE rag_index_queue`); err != nil {
		t.Fatalf("truncate rag_index_queue: %v", err)
	}
	return pool
}

func ctx(t *testing.T) context.Context { t.Helper(); return context.Background() }

func TestEnqueueAndClaim(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "11111111-1111-1111-1111-111111111111"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimPending(ctx(t), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].SourceID != recID || jobs[0].Op != "upsert" {
		t.Fatalf("unexpected claim: %+v", jobs)
	}
}

func TestClaimPendingSkipsInflightAndDone(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "22222222-2222-2222-2222-222222222222"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("first claim = %+v, %v; want 1 job", jobs, err)
	}
	// A second claim must not re-return the now-inflight job.
	jobs2, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs2) != 0 {
		t.Fatalf("second claim = %+v, %v; want 0 jobs (already inflight)", jobs2, err)
	}
	if err := q.Complete(ctx(t), jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	jobs3, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs3) != 0 {
		t.Fatalf("third claim = %+v, %v; want 0 jobs (already done)", jobs3, err)
	}
}

func TestFailReturnsToPendingUntilAttemptsExhausted(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "33333333-3333-3333-3333-333333333333"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	var id string
	for i := 0; i < maxAttempts; i++ {
		jobs, err := q.ClaimPending(ctx(t), 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim attempt %d = %+v, %v", i, jobs, err)
		}
		id = jobs[0].ID
		if err := q.Fail(ctx(t), id); err != nil {
			t.Fatal(err)
		}
	}
	// After maxAttempts failed attempts, the job must stop being reclaimed (status='error').
	jobs, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("claim after exhaustion = %+v, %v; want 0 jobs", jobs, err)
	}
	var status string
	if err := pool.QueryRow(ctx(t), `SELECT status FROM rag_index_queue WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("status = %q, want error", status)
	}
}

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

func TestStatsReportsPendingCountAndOldestAge(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	pending, age, err := q.Stats(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 || age != 0 {
		t.Fatalf("empty queue stats = (%d, %f), want (0, 0)", pending, age)
	}

	const recID = "44444444-4444-4444-4444-444444444444"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	pending, age, err = q.Stats(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	if age < 0 {
		t.Fatalf("age = %f, want >= 0", age)
	}

	// Claiming (moving to 'inflight') must remove it from the pending count.
	if _, err := q.ClaimPending(ctx(t), 10); err != nil {
		t.Fatal(err)
	}
	pending, _, err = q.Stats(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending after claim = %d, want 0", pending)
	}
}

// TestEnqueueDedupesAgainstExistingPending proves the reconciliation sweep
// re-deriving the same (source, op) pair on every maintenance tick does not
// pile up duplicate pending rows.
func TestEnqueueDedupesAgainstExistingPending(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "55555555-5555-5555-5555-555555555555"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	pending, _, err := q.Stats(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1 (duplicate enqueue must be skipped)", pending)
	}

	// A different op for the same source is NOT a duplicate.
	if err := q.Enqueue(ctx(t), recID, "delete"); err != nil {
		t.Fatal(err)
	}
	pending, _, err = q.Stats(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("pending = %d, want 2 (different op is a distinct job)", pending)
	}

	// Once the first job is claimed (no longer 'pending'), enqueueing the
	// same source+op again must not be treated as a duplicate.
	if _, err := q.ClaimPending(ctx(t), 10); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	pending, _, err = q.Stats(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1 (both prior jobs are now inflight, so a fresh upsert is not a duplicate)", pending)
	}
}

// TestReleaseReturnsToPendingWithoutChargingAnAttempt proves the retry-burn
// fix: Release must undo the attempt ClaimPending charged, not just flip the
// status back to pending.
func TestReleaseReturnsToPendingWithoutChargingAnAttempt(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "66666666-6666-6666-6666-666666666666"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %+v, %v; want 1 job", jobs, err)
	}
	if err := q.Release(ctx(t), jobs[0].ID); err != nil {
		t.Fatal(err)
	}

	var status string
	var attempts int
	if err := pool.QueryRow(ctx(t), `SELECT status, attempts FROM rag_index_queue WHERE id=$1`, jobs[0].ID).
		Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (Release must not charge a retry attempt)", attempts)
	}

	// The job must be claimable again — and this can repeat indefinitely
	// (Release never exhausts attempts, unlike Fail).
	jobs2, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs2) != 1 {
		t.Fatalf("re-claim after release = %+v, %v; want 1 job", jobs2, err)
	}
}

// TestReclaimStuckResetsOldInflightJobs proves a worker crash between Claim
// and Complete/Fail/Release does not strand a job 'inflight' forever.
func TestReclaimStuckResetsOldInflightJobs(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "77777777-7777-7777-7777-777777777777"
	if err := q.Enqueue(ctx(t), recID, "upsert"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimPending(ctx(t), 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %+v, %v; want 1 job", jobs, err)
	}

	// Freshly claimed: not yet stale, ReclaimStuck must leave it alone.
	n, err := q.ReclaimStuck(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reclaimed = %d, want 0 (job was just claimed)", n)
	}

	// Backdate claimed_at past reclaimStaleAfter to simulate an abandoned claim.
	if _, err := pool.Exec(ctx(t),
		`UPDATE rag_index_queue SET claimed_at = NOW() - INTERVAL '11 minutes' WHERE id = $1`, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	n, err = q.ReclaimStuck(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reclaimed = %d, want 1", n)
	}

	var status string
	if err := pool.QueryRow(ctx(t), `SELECT status FROM rag_index_queue WHERE id=$1`, jobs[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
}

// TestReviveResetsErrorRowsToPending proves the catch-up path: jobs that
// exhausted maxAttempts while the assistant was unavailable get a fresh set
// of attempts once it's re-enabled.
func TestReviveResetsErrorRowsToPending(t *testing.T) {
	pool := newTestPool(t)
	q := NewQueue(pool)

	const recID = "88888888-8888-8888-8888-888888888888"
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
	var status string
	if err := pool.QueryRow(ctx(t), `SELECT status FROM rag_index_queue WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("status = %q, want error (precondition)", status)
	}

	n, err := q.Revive(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("revived = %d, want 1", n)
	}

	var attempts int
	if err := pool.QueryRow(ctx(t), `SELECT status, attempts FROM rag_index_queue WHERE id=$1`, id).
		Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 0 {
		t.Fatalf("after revive: status=%q attempts=%d, want pending/0", status, attempts)
	}
}

// Package index owns RAG ingestion: the durable outbox queue and the async
// worker that turns record changes into fresh vectors.
package index

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Job is one pending index unit.
type Job struct {
	ID       string
	SourceID string // workflow record external id (what crmstore.GetRecord accepts)
	Op       string // "upsert" | "delete"
}

// Queue is the durable outbox backing near-real-time indexing.
type Queue struct{ pool *pgxpool.Pool }

// NewQueue builds a Queue over a tenant pool.
func NewQueue(pool *pgxpool.Pool) *Queue { return &Queue{pool: pool} }

// Enqueue records an index job, unless a 'pending' job for the same
// source+op already exists. Called by the store decorator after a write and
// by the reconciliation sweep, which re-derives the same (source, op) pairs
// on every run — without the dedupe check a backlog (or a paused/unavailable
// AI switch) would let duplicate pending rows for the same record pile up on
// every maintenance tick. A partial unique index would be cleaner, but
// existing tenants may already carry duplicate 'pending' rows (pre-dating
// this dedupe), and CREATE UNIQUE INDEX on a table with duplicates fails —
// which would block boot. WHERE NOT EXISTS is racy under true concurrent
// enqueues for the same (source, op), but the two callers here (index-on-write,
// the single-threaded-per-tenant reconcile sweep) make that an acceptable trade.
func (q *Queue) Enqueue(ctx context.Context, sourceID, op string) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO rag_index_queue (source_id, op)
		SELECT $1, $2
		WHERE NOT EXISTS (
			SELECT 1 FROM rag_index_queue
			WHERE source_id = $1 AND op = $2 AND status = 'pending')`,
		sourceID, op)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// ClaimPending atomically marks up to n pending jobs as in-flight and returns
// them (SKIP LOCKED so concurrent workers don't double-process). Status values:
// 'pending' | 'inflight' | 'done' | 'error' (free-text column, no DDL needed).
// claimed_at is stamped so a crash between Claim and Complete/Fail/Release
// (which would otherwise strand the row 'inflight' forever) can be found by
// ReclaimStuck.
func (q *Queue) ClaimPending(ctx context.Context, n int) ([]Job, error) {
	rows, err := q.pool.Query(ctx, `
		UPDATE rag_index_queue SET status='inflight', attempts=attempts+1, claimed_at=NOW()
		WHERE id IN (
			SELECT id FROM rag_index_queue WHERE status='pending'
			ORDER BY enqueued_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING id, source_id, op`, n)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.SourceID, &j.Op); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// Complete marks a job done.
func (q *Queue) Complete(ctx context.Context, id string) error {
	_, err := q.pool.Exec(ctx, `UPDATE rag_index_queue SET status='done' WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("complete job %s: %w", id, err)
	}
	return nil
}

// maxAttempts bounds queue-level retries. Kept generous relative to the
// embedder's own transport-level retries (see ai.OllamaEmbedder.postJSON) so a
// job surviving a scale-to-zero autostart/autostop race gets enough chances
// to land instead of going to 'error' (which is terminal — never reclaimed).
const maxAttempts = 10

// Fail returns a job to pending for retry, bounded by maxAttempts.
func (q *Queue) Fail(ctx context.Context, id string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE rag_index_queue
		SET status = CASE WHEN attempts >= $2 THEN 'error' ELSE 'pending' END
		WHERE id=$1`, id, maxAttempts)
	if err != nil {
		return fmt.Errorf("fail job %s: %w", id, err)
	}
	return nil
}

// Release returns a job to pending WITHOUT charging a retry attempt. Used
// when a job failed because the embedder itself was unreachable (see
// rag.ErrUnavailable in worker.go) rather than because of anything wrong
// with the job — that failure mode can burn through every one of
// maxAttempts's retries in seconds (the drain loop runs every few seconds)
// and land the job in the terminal 'error' state before Ollama has even had
// a chance to come back up.
func (q *Queue) Release(ctx context.Context, id string) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE rag_index_queue SET status='pending', attempts=GREATEST(attempts-1, 0) WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("release job %s: %w", id, err)
	}
	return nil
}

// reclaimStaleAfter is how long a job may sit 'inflight' before ReclaimStuck
// treats it as abandoned (the worker that claimed it crashed or was
// redeployed before it could Complete/Fail/Release it).
const reclaimStaleAfter = 10 * time.Minute

// ReclaimStuck resets 'inflight' jobs claimed more than reclaimStaleAfter ago
// back to 'pending', and reports how many it reclaimed. Called from the
// maintenance loop so a crashed worker's in-flight jobs are not stranded
// forever (rows claimed before claimed_at existed have it NULL and are left
// alone — no worse than before this existed).
func (q *Queue) ReclaimStuck(ctx context.Context) (int, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE rag_index_queue
		SET status = 'pending'
		WHERE status = 'inflight'
		  AND claimed_at IS NOT NULL
		  AND claimed_at < NOW() - ($1 * INTERVAL '1 second')`,
		reclaimStaleAfter.Seconds())
	if err != nil {
		return 0, fmt.Errorf("reclaim stuck jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// doneRetention is how long 'done' jobs are kept before PurgeDone deletes them.
const doneRetention = 7 * 24 * time.Hour

// PurgeDone deletes 'done' jobs older than doneRetention and reports how many
// it removed. The table has no completion timestamp, so age is measured from
// claimed_at (the last claim, i.e. when the job ran), falling back to
// enqueued_at for rows claimed before claimed_at existed.
func (q *Queue) PurgeDone(ctx context.Context) (int, error) {
	tag, err := q.pool.Exec(ctx, `
		DELETE FROM rag_index_queue
		WHERE status = 'done'
		  AND COALESCE(claimed_at, enqueued_at) < NOW() - ($1 * INTERVAL '1 second')`,
		doneRetention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("purge done jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Revive resets every terminal 'error' job back to 'pending' with attempts
// reset to 0, and reports how many it revived. Called on the
// unavailable-to-available transition (AI re-enabled, or Ollama confirmed
// back up) so jobs that exhausted their retries while the assistant was down
// get another full set of attempts instead of staying stuck forever.
func (q *Queue) Revive(ctx context.Context) (int, error) {
	tag, err := q.pool.Exec(ctx,
		`UPDATE rag_index_queue SET status='pending', attempts=0 WHERE status='error'`)
	if err != nil {
		return 0, fmt.Errorf("revive error jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Stats reports the current pending backlog: how many jobs are pending and
// the age in seconds of the oldest one (0 when nothing is pending) — an
// observability signal (see metrics.SetRAGIndexQueueStats) for whether
// indexing is falling behind, not something the drain path itself needs.
func (q *Queue) Stats(ctx context.Context) (pending int, oldestPendingAgeSeconds float64, err error) {
	err = q.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(enqueued_at))), 0)
		FROM rag_index_queue WHERE status = 'pending'`).Scan(&pending, &oldestPendingAgeSeconds)
	if err != nil {
		return 0, 0, fmt.Errorf("queue stats: %w", err)
	}
	return pending, oldestPendingAgeSeconds, nil
}

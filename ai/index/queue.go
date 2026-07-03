// Package index owns RAG ingestion: the durable outbox queue and the async
// worker that turns record changes into fresh vectors.
package index

import (
	"context"
	"fmt"

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

// Enqueue records an index job. Called by the store decorator after a write.
func (q *Queue) Enqueue(ctx context.Context, sourceID, op string) error {
	_, err := q.pool.Exec(ctx,
		`INSERT INTO rag_index_queue (source_id, op) VALUES ($1, $2)`, sourceID, op)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// ClaimPending atomically marks up to n pending jobs as in-flight and returns
// them (SKIP LOCKED so concurrent workers don't double-process). Status values:
// 'pending' | 'inflight' | 'done' | 'error' (free-text column, no DDL needed).
func (q *Queue) ClaimPending(ctx context.Context, n int) ([]Job, error) {
	rows, err := q.pool.Query(ctx, `
		UPDATE rag_index_queue SET status='inflight', attempts=attempts+1
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
	return err
}

// Fail returns a job to pending for retry, bounded by attempts (5 tries).
func (q *Queue) Fail(ctx context.Context, id string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE rag_index_queue
		SET status = CASE WHEN attempts >= 5 THEN 'error' ELSE 'pending' END
		WHERE id=$1`, id)
	return err
}

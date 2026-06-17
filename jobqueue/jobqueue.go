// Package jobqueue implements a durable, Postgres-backed job queue for
// long-running/async work (tenant provisioning, future workflow transition
// actions, etc.). Jobs are claimed with `SELECT ... FOR UPDATE SKIP LOCKED`,
// which makes it safe for multiple worker instances to poll the same queue.
package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status values for async_jobs.status.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusDead      = "dead"
)

// ErrNoJob is returned by ClaimNext when no pending job is available.
var ErrNoJob = errors.New("no pending job")

// Job is a row from async_jobs.
type Job struct {
	ID             string
	JobType        string
	TenantID       *string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	MaxAttempts    int
	LastError      *string
	IdempotencyKey *string
	Progress       json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Queue wraps the control-plane pool with job-queue operations.
type Queue struct {
	pool *pgxpool.Pool
}

// New builds a Queue backed by the given pool (the control-plane pool).
func New(pool *pgxpool.Pool) *Queue {
	return &Queue{pool: pool}
}

const jobColumns = `id, job_type, tenant_id, payload, status, attempts, max_attempts, last_error, idempotency_key, progress, created_at, updated_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	if err := row.Scan(
		&j.ID, &j.JobType, &j.TenantID, &j.Payload, &j.Status,
		&j.Attempts, &j.MaxAttempts, &j.LastError, &j.IdempotencyKey,
		&j.Progress, &j.CreatedAt, &j.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &j, nil
}

// Enqueue inserts a pending job. If idempotencyKey is non-empty and a job
// with that key already exists, Enqueue is a no-op and returns the existing
// job's ID instead of creating a duplicate.
func (q *Queue) Enqueue(ctx context.Context, jobType string, tenantID string, payload any, idempotencyKey string) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal job payload: %w", err)
	}

	var tenantIDArg any
	if tenantID != "" {
		tenantIDArg = tenantID
	}
	var idemArg any
	if idempotencyKey != "" {
		idemArg = idempotencyKey
	}

	var id string
	err = q.pool.QueryRow(ctx, `
		INSERT INTO async_jobs (job_type, tenant_id, payload, idempotency_key)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (idempotency_key) DO UPDATE SET updated_at = async_jobs.updated_at
		RETURNING id`,
		jobType, tenantIDArg, body, idemArg).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("enqueue job %s: %w", jobType, err)
	}
	return id, nil
}

// ClaimNext atomically claims the oldest pending job whose type is in
// jobTypes, marking it 'running' and incrementing its attempt counter.
// Returns ErrNoJob if nothing is pending.
func (q *Queue) ClaimNext(ctx context.Context, jobTypes []string) (*Job, error) {
	row := q.pool.QueryRow(ctx, `
		UPDATE async_jobs
		SET status = $2, attempts = attempts + 1, updated_at = NOW()
		WHERE id = (
			SELECT id FROM async_jobs
			WHERE status = $3 AND job_type = ANY($1)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+jobColumns, jobTypes, StatusRunning, StatusPending)

	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoJob
	}
	if err != nil {
		return nil, fmt.Errorf("claim next job: %w", err)
	}
	return j, nil
}

// MarkSucceeded marks a job as completed successfully.
func (q *Queue) MarkSucceeded(ctx context.Context, id string) error {
	if _, err := q.pool.Exec(ctx,
		`UPDATE async_jobs SET status = $2, last_error = NULL, updated_at = NOW() WHERE id = $1`,
		id, StatusSucceeded); err != nil {
		return fmt.Errorf("mark job %s succeeded: %w", id, err)
	}
	return nil
}

// MarkFailed records the error and either resets the job to 'pending' (for
// the worker to retry later) or moves it to 'dead' once max_attempts is
// reached.
func (q *Queue) MarkFailed(ctx context.Context, id string, errMsg string) error {
	if _, err := q.pool.Exec(ctx, `
		UPDATE async_jobs
		SET status = CASE WHEN attempts >= max_attempts THEN $3 ELSE $4 END,
		    last_error = $2,
		    updated_at = NOW()
		WHERE id = $1`,
		id, errMsg, StatusDead, StatusPending); err != nil {
		return fmt.Errorf("mark job %s failed: %w", id, err)
	}
	return nil
}

// UpdateProgress stores a JSON progress marker (e.g. {"step": "migrate_db"})
// so a resumed job can report what's already been done.
func (q *Queue) UpdateProgress(ctx context.Context, id string, progress any) error {
	body, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("marshal job progress: %w", err)
	}
	if _, err := q.pool.Exec(ctx,
		`UPDATE async_jobs SET progress = $2, updated_at = NOW() WHERE id = $1`,
		id, body); err != nil {
		return fmt.Errorf("update job %s progress: %w", id, err)
	}
	return nil
}

// RequeueStale resets jobs stuck in 'running' (e.g. due to a worker crash)
// back to 'pending' if they haven't been updated within staleAfter. Returns
// the number of jobs requeued.
func (q *Queue) RequeueStale(ctx context.Context, staleAfter time.Duration) (int64, error) {
	threshold := time.Now().Add(-staleAfter)
	tag, err := q.pool.Exec(ctx, `
		UPDATE async_jobs
		SET status = $2, updated_at = NOW()
		WHERE status = $3 AND updated_at < $1`,
		threshold, StatusPending, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("requeue stale jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Retry resets a failed/dead job back to pending for a manual retry
// (e.g. triggered by a platform admin from the UI).
func (q *Queue) Retry(ctx context.Context, id string) error {
	tag, err := q.pool.Exec(ctx, `
		UPDATE async_jobs
		SET status = $2, attempts = 0, last_error = NULL, updated_at = NOW()
		WHERE id = $1 AND status = ANY($3)`,
		id, StatusPending, []string{StatusFailed, StatusDead})
	if err != nil {
		return fmt.Errorf("retry job %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("retry job %s: not found or not in a retryable state", id)
	}
	return nil
}

// ListForTenant returns the most recent jobs for a tenant, newest first.
func (q *Queue) ListForTenant(ctx context.Context, tenantID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := q.pool.Query(ctx,
		`SELECT `+jobColumns+` FROM async_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`,
		tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs for tenant %s: %w", tenantID, err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job row: %w", err)
		}
		jobs = append(jobs, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

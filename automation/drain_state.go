package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// execer is the pgx surface the state updates need (pool, conn or tx).
type execer interface {
	Querier
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// sweepSuperseded skips unsent rows of the rule (including claimed rows whose
// lease has expired) when a later stage for the same
// record and anchor is already planned, in flight, failed, sent or unknown.
func sweepSuperseded(ctx context.Context, q execer, ruleID string, now time.Time) error {
	if _, err := q.Exec(ctx, `
UPDATE automation_deliveries d
SET status = 'skipped', reason = $3, lease_until = NULL, updated_at = $2::timestamptz
WHERE d.rule_id = $1::uuid
  AND (d.status IN ('planned', 'failed') OR (d.status = 'claimed' AND d.lease_until < $2::timestamptz))
  AND EXISTS (
    SELECT 1 FROM automation_deliveries h
    WHERE h.rule_id = d.rule_id AND h.resource = d.resource AND h.record_id = d.record_id
      AND h.anchor_date = d.anchor_date AND h.stage > d.stage
      AND h.status IN ('planned', 'claimed', 'failed', 'sent', 'unknown'))`,
		ruleID, now, reasonSuperseded); err != nil {
		return fmt.Errorf("drain supersede sweep: %w", err)
	}
	return nil
}

// sweepStale skips unsent rows created more than staleAfter ago. It keys off
// created_at, not run_at, because deferral pushes run_at forward.
func sweepStale(ctx context.Context, q execer, ruleID string, now time.Time) error {
	if _, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = 'skipped', reason = $4, updated_at = $2::timestamptz
WHERE rule_id = $1::uuid AND status IN ('planned', 'failed') AND created_at < $3::timestamptz`,
		ruleID, now, now.Add(-staleAfter), reasonStale); err != nil {
		return fmt.Errorf("drain stale sweep: %w", err)
	}
	return nil
}

// claimDue leases due (or lease-expired) rows to this drain, a whole digest at
// a time: digests are taken in order of earliest run_at while their combined
// row count stays within claimBatchSize (the first digest is always taken whole,
// however large), so one digest is never split across two emails. Rows without
// a digest_key are each their own unit.
func claimDue(ctx context.Context, q execer, ruleID string, now time.Time) ([]Delivery, error) {
	rows, err := q.Query(ctx, `
WITH due AS (
    SELECT id, COALESCE(digest_key, 'single:' || id::text) AS unit, run_at
    FROM automation_deliveries
    WHERE rule_id = $1::uuid
      AND ((status IN ('planned', 'failed') AND run_at <= $2::timestamptz)
        OR (status = 'claimed' AND lease_until < $2::timestamptz))
      AND attempts < $4
    FOR UPDATE SKIP LOCKED
), units AS (
    SELECT unit, MIN(run_at) AS first_run, count(*) AS n FROM due GROUP BY unit
), ranked AS (
    SELECT unit, n, SUM(n) OVER (ORDER BY first_run, unit) AS running FROM units
), chosen AS (
    SELECT unit FROM ranked WHERE running <= $5 OR running = n
)
UPDATE automation_deliveries
SET status = 'claimed', lease_until = $3::timestamptz, updated_at = $2::timestamptz
WHERE id IN (SELECT d.id FROM due d JOIN chosen c ON c.unit = d.unit)
RETURNING id::text, rule_id::text, resource, record_id, occurrence_key,
          COALESCE(digest_key, ''), recipient_email, anchor_date, attempts`,
		ruleID, now, now.Add(claimLease), maxAttempts, claimBatchSize)
	if err != nil {
		return nil, fmt.Errorf("drain claim: %w", err)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.RuleID, &d.Resource, &d.RecordID, &d.OccurrenceKey,
			&d.DigestKey, &d.RecipientEmail, &d.Anchor, &d.Attempts); err != nil {
			return nil, fmt.Errorf("drain claim scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("drain claim rows: %w", err)
	}
	return out, nil
}

// markSkipped moves still-claimed rows to skipped with the given reason.
func markSkipped(ctx context.Context, q execer, ids []string, reason string, now time.Time) (int, error) {
	tag, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = 'skipped', reason = $2, lease_until = NULL, updated_at = $3::timestamptz
WHERE id = ANY($1::uuid[]) AND status = 'claimed'`, ids, reason, now)
	if err != nil {
		return 0, fmt.Errorf("drain mark skipped: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// markSent records a successful send on still-claimed rows.
func markSent(ctx context.Context, q execer, ids, notifyIDs []string, now time.Time) (int, error) {
	if notifyIDs == nil {
		notifyIDs = []string{}
	}
	tag, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = 'sent', sent_at = $3::timestamptz, notify_notification_ids = $2::text[],
    lease_until = NULL, updated_at = $3::timestamptz
WHERE id = ANY($1::uuid[]) AND status = 'claimed'`, ids, notifyIDs, now)
	if err != nil {
		return 0, fmt.Errorf("drain mark sent: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// markUnknown records a send whose outcome is uncertain (timeout); never retried.
func markUnknown(ctx context.Context, q execer, ids []string, msg string, now time.Time) (int, error) {
	tag, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = 'unknown', last_error = $2, lease_until = NULL, updated_at = $3::timestamptz
WHERE id = ANY($1::uuid[]) AND status = 'claimed'`, ids, truncateError(msg), now)
	if err != nil {
		return 0, fmt.Errorf("drain mark unknown: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// markSendFailed bumps attempts on each item and moves it to failed (with a
// backoff run_at) or, at maxAttempts, dead. Result.Failed counts every row.
func markSendFailed(ctx context.Context, q execer, items []Delivery, msg string, now time.Time, res *Result) error {
	var errs error
	for _, it := range items {
		attempts := it.Attempts + 1
		status := statusFailed
		if attempts >= maxAttempts {
			status = statusDead
		}
		tag, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = $2, attempts = $3, run_at = $4::timestamptz, last_error = $5,
    lease_until = NULL, updated_at = $6::timestamptz
WHERE id = $1::uuid AND status = 'claimed'`,
			it.ID, status, attempts, now.Add(backoffFor(attempts)), truncateError(msg), now)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("drain mark failed: %w", err))
			continue
		}
		res.Failed += int(tag.RowsAffected())
	}
	return errs
}

// releaseAfter puts a group's still-claimed rows back to planned (attempts
// unchanged, lease cleared) and returns cause wrapped with stage context.
func releaseAfter(ctx context.Context, q execer, items []Delivery, now time.Time, cause error, stage string) error {
	dctx, cancel := detach(ctx)
	defer cancel()
	err := releaseRows(dctx, q, items, now)
	out := fmt.Errorf("drain %s: %w", stage, cause)
	if err != nil {
		out = errors.Join(out, err)
	}
	return out
}

// releaseRows puts still-claimed rows back to planned (attempts unchanged,
// lease cleared) without an error cause; used when the drain runs out of time.
func releaseRows(ctx context.Context, q execer, items []Delivery, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = 'planned', lease_until = NULL, updated_at = $2::timestamptz
WHERE id = ANY($1::uuid[]) AND status = 'claimed'`, deliveryIDs(items), now); err != nil {
		return fmt.Errorf("drain release: %w", err)
	}
	return nil
}

// deferRows puts still-claimed rows back to planned with run_at pushed to runAt
// (attempts unchanged, lease cleared), so a guard hit retries later, not never.
func deferRows(ctx context.Context, q execer, items []Delivery, runAt, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
UPDATE automation_deliveries
SET status = 'planned', run_at = $2::timestamptz, lease_until = NULL, updated_at = $3::timestamptz
WHERE id = ANY($1::uuid[]) AND status = 'claimed'`, deliveryIDs(items), runAt, now); err != nil {
		return fmt.Errorf("drain defer: %w", err)
	}
	return nil
}

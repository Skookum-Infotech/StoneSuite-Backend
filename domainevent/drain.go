package domainevent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/metrics"
	"stonesuite-backend/provenance"
)

const (
	ensureCursor = `INSERT INTO domain_event_cursor (consumer) VALUES ($1) ON CONFLICT DO NOTHING`
	lockCursor   = `SELECT last_id, failing_id, failures FROM domain_event_cursor
WHERE consumer = $1 FOR UPDATE SKIP LOCKED`
	selectEvents = `SELECT id, event_type, resource, record_id, actor_user_id::text, via, depth, payload, created_at
FROM domain_event WHERE id > $1 ORDER BY id LIMIT $2`
	advanceCursor = `UPDATE domain_event_cursor SET last_id = $2, failing_id = NULL, failures = 0, updated_at = now()
WHERE consumer = $1`
	recordFailure = `UPDATE domain_event_cursor SET failing_id = $2, failures = $3, updated_at = now()
WHERE consumer = $1`
)

// DrainResult summarises one drain call.
type DrainResult struct {
	Handled int
	// Skipped counts dead-lettered events (failed MaxAttempts times).
	Skipped int
	// Busy is true when another drainer held the consumer's cursor.
	Busy bool
	// Failed is true when a handler failed and the cursor was left in place.
	Failed bool
}

// Drain processes up to batch events for one consumer in a single transaction.
// Only one drainer per consumer per tenant runs at a time; others return Busy.
func Drain(ctx context.Context, pool *pgxpool.Pool, reg *Registry, consumer string, batch int) (DrainResult, error) {
	var res DrainResult
	h, ok := reg.Handler(consumer)
	if !ok {
		return res, fmt.Errorf("drain: unknown consumer %q", consumer)
	}
	if batch <= 0 {
		batch = DefaultBatch
	}
	// Autocommitted so concurrent drainers never block on each other's insert.
	if _, err := pool.Exec(ctx, ensureCursor, consumer); err != nil {
		return res, fmt.Errorf("drain %s: ensure cursor: %w", consumer, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("drain %s: begin: %w", consumer, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	var (
		lastID    int64
		failingID *int64
		failures  int
	)
	if err := tx.QueryRow(ctx, lockCursor, consumer).Scan(&lastID, &failingID, &failures); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Busy = true
			return res, nil
		}
		return res, fmt.Errorf("drain %s: lock cursor: %w", consumer, err)
	}
	events, err := loadEvents(ctx, tx, lastID, batch)
	if err != nil {
		return res, fmt.Errorf("drain %s: %w", consumer, err)
	}
	for _, ev := range events {
		herr := runHandler(ctx, tx, h, ev)
		if herr == nil {
			if _, err := tx.Exec(ctx, advanceCursor, consumer, ev.ID); err != nil {
				return res, fmt.Errorf("drain %s: advance cursor: %w", consumer, err)
			}
			res.Handled++
			continue
		}
		metrics.DomainEventHandlerFailed(consumer)
		if failingID == nil || *failingID != ev.ID {
			failures = 0
		}
		failures++
		id := ev.ID
		failingID = &id
		if failures >= MaxAttempts {
			slog.ErrorContext(ctx, "domain event dead-lettered", "consumer", consumer,
				"event_id", ev.ID, "event_type", ev.Type, "resource", ev.Resource, "attempts", failures, "error", herr)
			metrics.DomainEventDeadLettered(consumer)
			if _, err := tx.Exec(ctx, advanceCursor, consumer, ev.ID); err != nil {
				return res, fmt.Errorf("drain %s: skip event: %w", consumer, err)
			}
			failingID, failures = nil, 0
			res.Skipped++
			continue
		}
		slog.WarnContext(ctx, "domain event handler failed", "consumer", consumer,
			"event_id", ev.ID, "event_type", ev.Type, "attempt", failures, "error", herr)
		if _, err := tx.Exec(ctx, recordFailure, consumer, ev.ID, failures); err != nil {
			return res, fmt.Errorf("drain %s: record failure: %w", consumer, err)
		}
		res.Failed = true
		break
	}
	if err := tx.Commit(ctx); err != nil {
		return DrainResult{}, fmt.Errorf("drain %s: commit: %w", consumer, err)
	}
	return res, nil
}

// runHandler runs h in a savepoint; errors and panics roll the savepoint back.
func runHandler(ctx context.Context, tx pgx.Tx, h Handler, ev Stored) (err error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin savepoint: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
		if err != nil {
			if rbErr := sp.Rollback(ctx); rbErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback savepoint: %w", rbErr))
			}
			return
		}
		if cErr := sp.Commit(ctx); cErr != nil {
			err = fmt.Errorf("release savepoint: %w", cErr)
		}
	}()
	return h(ctx, sp, ev)
}

func loadEvents(ctx context.Context, tx pgx.Tx, after int64, limit int) ([]Stored, error) {
	rows, err := tx.Query(ctx, selectEvents, after, limit)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	defer rows.Close()
	var out []Stored
	for rows.Next() {
		var (
			s     Stored
			actor *string
			via   string
		)
		if err := rows.Scan(&s.ID, &s.Type, &s.Resource, &s.RecordID, &actor, &via, &s.Depth, &s.Payload, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if actor != nil {
			s.ActorUserID = *actor
		}
		s.Via = provenance.Via(via)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	return out, nil
}

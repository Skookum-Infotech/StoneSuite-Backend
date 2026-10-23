package proposedaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/provenance"
)

// nowFunc is the clock used for the in-Go expiry check; the atomic flip
// re-checks expiry in SQL, so this is only a fast path.
func nowFunc() time.Time { return time.Now() }

// viaFor maps a proposal source to the provenance its writes carry.
func viaFor(src Source) provenance.Via {
	if src == SourceDocExtract {
		return provenance.DocExtract
	}
	return provenance.AIAgent
}

// failAfterRollback rolls tx back (releasing the row lock), then records the
// proposal as failed with a sanitized message in a separate small transaction.
func (s *Store) failAfterRollback(ctx context.Context, tx pgx.Tx, id, msg string, cause error) (Action, bool, error) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.Error("rollback before marking proposed action failed", "action_id", id, "error", err)
	}
	if err := s.markFailed(context.WithoutCancel(ctx), id, msg); err != nil {
		slog.Error("mark proposed action failed", "action_id", id, "error", err)
		return Action{}, false, errors.Join(cause, err)
	}
	return Action{}, false, cause
}

// markFailed sets a pending row to failed with a fixed, sanitized message.
func (s *Store) markFailed(ctx context.Context, id, msg string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark failed: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	if _, err := tx.Exec(ctx, `
		UPDATE ai_proposed_action
		SET status = 'failed', error_message = $2, resolved_at = now()
		WHERE id = $1 AND status = 'pending'`, id, msg); err != nil {
		return fmt.Errorf("mark failed: update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mark failed: commit: %w", err)
	}
	return nil
}

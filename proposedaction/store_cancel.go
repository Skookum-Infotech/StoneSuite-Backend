package proposedaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Cancel atomically moves a caller's pending proposal to cancelled. Cancelling
// an already-cancelled one is idempotent; any other non-pending state is
// ErrConflict, and an overdue pending row is ErrExpired.
func (s *Store) Cancel(ctx context.Context, identityID, id string) (Action, error) {
	if !uuidPattern.MatchString(id) {
		return Action{}, &NotFoundError{}
	}
	a, err := scanAction(s.pool.QueryRow(ctx, `
		UPDATE ai_proposed_action SET status = 'cancelled', resolved_at = now()
		WHERE id = $1 AND identity_id = $2 AND status = 'pending' AND expires_at > now()
		RETURNING `+actionColumns, id, identityID))
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Action{}, fmt.Errorf("cancel proposed action: %w", err)
	}
	cur, err := s.Get(ctx, identityID, id) // also flips overdue pending to expired
	if err != nil {
		return Action{}, err
	}
	switch cur.Status {
	case StatusCancelled:
		return cur, nil
	case StatusExpired:
		return Action{}, ErrExpired
	default:
		return Action{}, ErrConflict
	}
}

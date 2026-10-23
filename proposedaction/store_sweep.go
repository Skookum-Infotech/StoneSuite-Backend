package proposedaction

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Sweep expires overdue pending rows and deletes resolved rows older than
// retentionDays. It reports how many rows each step touched.
func (s *Store) Sweep(ctx context.Context, pool *pgxpool.Pool, retentionDays int) (expired, deleted int64, err error) {
	tag, err := pool.Exec(ctx, `
		UPDATE ai_proposed_action SET status = 'expired', resolved_at = now()
		WHERE status = 'pending' AND expires_at <= now()`)
	if err != nil {
		return 0, 0, fmt.Errorf("expire proposed actions: %w", err)
	}
	expired = tag.RowsAffected()
	tag, err = pool.Exec(ctx, `
		DELETE FROM ai_proposed_action
		WHERE status <> 'pending'
		  AND resolved_at < now() - make_interval(days => $1::int)`, retentionDays)
	if err != nil {
		return expired, 0, fmt.Errorf("delete resolved proposed actions: %w", err)
	}
	return expired, tag.RowsAffected(), nil
}

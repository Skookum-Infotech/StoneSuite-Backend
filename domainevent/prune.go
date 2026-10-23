package domainevent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const pruneSQL = `DELETE FROM domain_event
WHERE created_at < now() - make_interval(days => $1)
  AND id <= COALESCE((SELECT MIN(last_id) FROM domain_event_cursor), id)`

// Prune deletes events older than retentionDays that every consumer cursor has
// already passed; with no cursors, age alone applies. It returns rows deleted.
func Prune(ctx context.Context, pool *pgxpool.Pool, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, errors.New("prune: retentionDays must be positive")
	}
	tag, err := pool.Exec(ctx, pruneSQL, retentionDays)
	if err != nil {
		return 0, fmt.Errorf("prune domain events: %w", err)
	}
	return tag.RowsAffected(), nil
}

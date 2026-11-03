// Package historywrite runs best-effort history inserts inside a caller's
// transaction without letting a failure poison it.
package historywrite

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// Exec runs a history INSERT inside a SAVEPOINT. A bare failing statement
// aborts the whole pgx transaction (every later statement and the COMMIT then
// fail with a misleading "current transaction is aborted"); the savepoint
// confines the failure to the history row, which is logged and dropped.
func Exec(ctx context.Context, tx pgx.Tx, sql string, args ...any) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		slog.Warn("history write: savepoint failed", "error", err)
		return
	}
	if _, err := sp.Exec(ctx, sql, args...); err != nil {
		slog.Warn("history write failed", "error", err)
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			slog.Warn("history write: rollback to savepoint failed", "error", rbErr)
		}
		return
	}
	if err := sp.Commit(ctx); err != nil {
		slog.Warn("history write: release savepoint failed", "error", err)
	}
}

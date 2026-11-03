package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// unlockTimeout bounds the advisory unlock, which runs on a context detached
// from the caller's so it still happens after the apply ctx is cancelled.
const unlockTimeout = 5 * time.Second

// applySchemaUnderLock applies schemaSQL in one transaction while holding the
// session-level advisory lock lockKey. Lock, transaction and unlock all run on
// a single acquired connection: pg_advisory_lock is per-session, so issuing the
// unlock through the pool could land on a different connection, fail silently,
// and leave the lock held on an idle pooled connection.
func applySchemaUnderLock(ctx context.Context, pool *pgxpool.Pool, lockKey int64, schemaSQL, label string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire %s schema conn: %w", label, err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("acquire %s schema lock: %w", label, err)
	}
	defer func() {
		// Detached ctx: the unlock must run even if ctx is done, or the lock
		// rides back into the pool still held.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
		defer cancel()
		if _, uerr := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, lockKey); uerr != nil {
			slog.Error("schema advisory unlock failed", "schema", label, "lock_key", lockKey, "error", uerr)
			// A connection whose unlock failed may still hold the lock; close
			// it instead of returning it to the pool.
			if cerr := conn.Conn().Close(uctx); cerr != nil {
				slog.Error("close schema lock conn failed", "schema", label, "error", cerr)
			}
		}
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s schema tx: %w", label, err)
	}
	if err := liftSessionTimeouts(ctx, tx); err != nil {
		if rerr := tx.Rollback(ctx); rerr != nil {
			slog.Error("rollback schema tx failed", "schema", label, "error", rerr)
		}
		return err
	}
	if _, err := tx.Exec(ctx, schemaSQL); err != nil {
		if rerr := tx.Rollback(ctx); rerr != nil {
			slog.Error("rollback schema tx failed", "schema", label, "error", rerr)
		}
		return fmt.Errorf("apply %s schema: %w", label, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s schema: %w", label, err)
	}
	return nil
}

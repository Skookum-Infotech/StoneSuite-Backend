package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// liftSessionTimeoutsSQL disables the pool-wide statement and lock limits
// (tenancy.SessionTimeouts) for the rest of a migration transaction only: the
// whole schema runs as one statement and takes ACCESS EXCLUSIVE locks, so the
// 30s/5s request-path limits would fail a legitimate boot. SET LOCAL ends with
// the transaction, so the pooled connection keeps its limits afterwards.
const liftSessionTimeoutsSQL = `SELECT set_config('statement_timeout', '0', true), set_config('lock_timeout', '0', true)`

// liftSessionTimeouts applies liftSessionTimeoutsSQL inside tx.
func liftSessionTimeouts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, liftSessionTimeoutsSQL); err != nil {
		return fmt.Errorf("lift session timeouts for migration: %w", err)
	}
	return nil
}

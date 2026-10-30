// bintransfer/write_safety.go — optimistic locking and idempotent create.
package bintransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this bin transfer was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces bin transfer creates in request_idempotency.
const idempotencyScopeCreate = "bin_transfer.create"

// checkExpectedVersion enforces optimistic locking when the client sent the
// version it loaded. No version means the caller predates the check; it keeps
// last-write-wins until every client sends one.
func checkExpectedVersion(expected *int, current int) error {
	if expected != nil && *expected != current {
		return ErrVersionConflict
	}
	return nil
}

// checkLockedVersion re-reads the version under the row lock the caller
// already holds and compares it with the client's expectation. It costs
// nothing when the client sent no version.
func checkLockedVersion(ctx context.Context, tx pgx.Tx, id int, expected *int) error {
	if expected == nil {
		return nil
	}
	var current int
	if err := tx.QueryRow(ctx, `
		SELECT bin_transfer_record_version FROM bin_transfer
		WHERE bin_transfer_id = $1`, id).Scan(&current); err != nil {
		return fmt.Errorf("read bin transfer version: %w", err)
	}
	return checkExpectedVersion(expected, current)
}

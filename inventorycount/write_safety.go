package inventorycount

// write_safety.go — optimistic locking, idempotent create and input bounds.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this count was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces count creates in request_idempotency.
const idempotencyScopeCreate = "inventory_count.create"

const (
	// maxCountEntries caps how many counted lines one request may record. Each
	// entry is an UPDATE under the document lock; an unbounded list holds that
	// lock and a connection for as long as the loop runs.
	maxCountEntries = 1000
	// maxQuantity is the largest magnitude a DECIMAL(14,3) quantity column holds.
	maxQuantity = 1e11 - 0.001
)

// validateCountedQty rejects a counted quantity that is negative, NaN, infinite
// or beyond what the quantity column holds.
func validateCountedQty(q float64) error {
	if !(q >= 0 && q <= maxQuantity) { // negated form also rejects NaN
		return ClientError{Msg: "A counted quantity must be zero or more, and within range."}
	}
	return nil
}

// validateCountEntries checks the entry-count and quantity limits of a
// RecordCounts request before any lock is taken.
func validateCountEntries(entries []CountEntry) error {
	if len(entries) > maxCountEntries {
		return ClientError{Msg: fmt.Sprintf("At most %d counts can be recorded in one request.", maxCountEntries)}
	}
	for i, e := range entries {
		if e.CountedQty == nil {
			continue
		}
		if err := validateCountedQty(*e.CountedQty); err != nil {
			return ClientError{Msg: fmt.Sprintf("Entry %d: %s", i+1, err.Error())}
		}
	}
	return nil
}

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
		SELECT count_record_version FROM inventory_count
		WHERE inventory_count_id = $1`, id).Scan(&current); err != nil {
		return fmt.Errorf("read count version: %w", err)
	}
	return checkExpectedVersion(expected, current)
}

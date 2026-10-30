package inventoryadjustment

// write_safety.go — optimistic locking, idempotent create and input bounds.

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this adjustment was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces adjustment creates in request_idempotency.
const idempotencyScopeCreate = "inventory_adjustment.create"

const (
	// maxLineItems caps the lines one adjustment may carry. Each line costs
	// several lookups and an insert; an unbounded list ties up a connection.
	maxLineItems = 500
	// maxQuantity is the largest magnitude a DECIMAL(14,3) quantity column holds.
	maxQuantity = 1e11 - 0.001
)

// validateWriteInput checks the line-count and quantity limits shared by
// create and update. The negated comparisons also reject NaN. A delta is
// signed, so only its magnitude is bounded; sign and zero stay with
// resolveLines, which knows whether the line is bulk or serialized.
func validateWriteInput(lines []LineInput) error {
	if len(lines) > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("An adjustment can have at most %d lines.", maxLineItems)}
	}
	for i, l := range lines {
		if !(math.Abs(l.QtyDelta) <= maxQuantity) { // negated form also rejects NaN
			return ClientError{Msg: fmt.Sprintf("Line %d: quantity is out of range.", i+1)}
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
		SELECT adjustment_record_version FROM inventory_adjustment
		WHERE inventory_adjustment_id = $1`, id).Scan(&current); err != nil {
		return fmt.Errorf("read adjustment version: %w", err)
	}
	return checkExpectedVersion(expected, current)
}

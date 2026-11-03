package cashtransfer

import (
	"errors"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this cash transfer was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces cash transfer creates in request_idempotency.
const idempotencyScopeCreate = "cashtransfer.create"

// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column holds.
const maxMoneyAmount = 1e13 - 0.01

// validateAmount rejects a non-positive, NaN, Inf or out-of-range amount.
func validateAmount(amount float64) error {
	if !(amount > 0 && amount <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "amount must be positive and within range."}
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

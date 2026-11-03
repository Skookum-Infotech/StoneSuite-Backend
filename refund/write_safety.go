package refund

import (
	"errors"
	"fmt"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this refund was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces refund creates in request_idempotency.
const idempotencyScopeCreate = "refund.create"

// voidStatusCode is the terminal status a voided refund carries.
const voidStatusCode = "VOID"

// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column holds.
// A larger value used to reach Postgres and come back as a 500.
const maxMoneyAmount = 1e13 - 0.01

// validateAmount checks a money amount is positive and within range. The
// negated form also rejects NaN.
func validateAmount(field string, amount float64) error {
	if !(amount > 0 && amount <= maxMoneyAmount) {
		return ClientError{Msg: fmt.Sprintf("%s must be positive and within range.", field)}
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

// expense/write_safety.go — input validation and concurrency guards shared by
// Create / Update / SoftDelete.
package expense

import (
	"errors"
	"fmt"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this expense claim was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces expense creates in request_idempotency.
const idempotencyScopeCreate = "expense.create"

// maxLineItems caps the lines on one expense claim.
const maxLineItems = 500

// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column holds.
// A larger value used to reach Postgres and come back as a 500.
const maxMoneyAmount = 1e13 - 0.01

// isoDate is the layout of a line's expense date.
const isoDate = "2006-01-02"

// validateLineAmount checks a line amount is within range. The negated form
// also rejects NaN, which every ordered comparison treats as false.
func validateLineAmount(lineNumber int, amount float64) error {
	if !(amount >= 0 && amount <= maxMoneyAmount) {
		return ClientError{Msg: fmt.Sprintf("Line %d: amount must be between 0 and the maximum allowed.", lineNumber)}
	}
	return nil
}

// validateLineDate checks a line's expense date is a real yyyy-mm-dd date.
func validateLineDate(lineNumber int, date string) error {
	if _, err := time.Parse(isoDate, date); err != nil {
		return ClientError{Msg: fmt.Sprintf("Line %d: expense date must be a valid yyyy-mm-dd date.", lineNumber)}
	}
	return nil
}

// validateLineCount caps the number of lines on one claim.
func validateLineCount(n int) error {
	if n > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("An expense claim can have at most %d lines.", maxLineItems)}
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

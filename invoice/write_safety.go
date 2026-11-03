package invoice

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this invoice was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces invoice creates in request_idempotency.
const idempotencyScopeCreate = "invoice.create"

const (
	// maxLineItems caps the lines one invoice may carry. Each line costs
	// several lookups and an insert; an unbounded list is a cheap way to tie
	// up a connection and the request for a long time.
	maxLineItems = 500
	// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column
	// holds. A larger value used to reach Postgres and come back as a 500.
	maxMoneyAmount = 1e13 - 0.01
	// isoDate is the yyyy-mm-dd layout dates arrive in.
	isoDate = "2006-01-02"
)

// validateWriteInput checks the header-level limits shared by create and
// update. invoiceDate is the effective date (the stored one when an update
// leaves it blank).
func validateWriteInput(lineCount int, shipping, adjustment float64, invoiceDate, dueDate string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("An invoice can have at most %d lines.", maxLineItems)}
	}
	if !(shipping >= 0 && shipping <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "shippingCharge must be zero or more, and within range."}
	}
	if !(math.Abs(adjustment) <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "adjustment is out of range."}
	}
	return validateDueDate(invoiceDate, dueDate)
}

// validateDueDate rejects a due date before the invoice date. Blank or
// unparseable dates are left to the database's own date handling.
func validateDueDate(invoiceDate, dueDate string) error {
	if invoiceDate == "" || dueDate == "" {
		return nil
	}
	inv, err1 := time.Parse(isoDate, invoiceDate)
	due, err2 := time.Parse(isoDate, dueDate)
	if err1 != nil || err2 != nil {
		return nil
	}
	if due.Before(inv) {
		return ClientError{Msg: "dueDate cannot be before invoiceDate."}
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

package requisition

import (
	"errors"
	"fmt"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this requisition was changed by someone else; reload it and try again")

// ErrHasDependents is returned when a delete is refused because the
// requisition has already been converted into a live purchase order.
var ErrHasDependents = errors.New("this requisition cannot be deleted")

// idempotencyScopeCreate namespaces requisition creates in request_idempotency.
const idempotencyScopeCreate = "requisition.create"

const (
	// maxLineItems caps the lines one requisition may carry. Each line costs
	// several lookups and an insert; an unbounded list is a cheap way to tie
	// up a connection and the request for a long time.
	maxLineItems = 500
	// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column
	// holds. A larger value used to reach Postgres and come back as a 500.
	maxMoneyAmount = 1e13 - 0.01
	// maxQuantity is the largest magnitude a DECIMAL(14,3) quantity holds.
	maxQuantity = 1e11 - 0.001
	// maxPercent is the upper bound of any percentage column.
	maxPercent = 100
	// isoDate is the yyyy-mm-dd layout dates arrive in.
	isoDate = "2006-01-02"
)

// validateWriteInput checks the header-level limits shared by create and
// update. Comparisons are in the negated form so NaN is rejected too.
func validateWriteInput(lineCount int, salesTaxPercent float64, neededByDate string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("A requisition can have at most %d lines.", maxLineItems)}
	}
	if !(salesTaxPercent >= 0 && salesTaxPercent <= maxPercent) {
		return ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	if neededByDate != "" {
		if _, err := time.Parse(isoDate, neededByDate); err != nil {
			return ClientError{Msg: "neededByDate must be a date in yyyy-mm-dd form."}
		}
	}
	return nil
}

// validateLineAmounts checks one line's typed numbers. Every comparison is in
// the negated form so NaN is rejected too. quantity is the effective quantity
// (already derived from dimensions when the line is sized).
func validateLineAmounts(lineNo int, quantity, unitPrice float64) error {
	if !(quantity > 0 && quantity <= maxQuantity) {
		return ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero, and within range.", lineNo)}
	}
	if !(unitPrice >= 0 && unitPrice <= maxMoneyAmount) {
		return ClientError{Msg: fmt.Sprintf("Line %d: estimated unit price must be zero or more, and within range.", lineNo)}
	}
	return nil
}

// validateTotals rejects a computed line or header amount that would not fit
// the money columns or would go negative.
func validateTotals(label string, amount float64) error {
	if !(amount >= 0 && amount <= maxMoneyAmount) {
		return ClientError{Msg: fmt.Sprintf("The %s must be zero or more, and within range.", label)}
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

// deletableStatus reports whether a requisition in statusCode may be deleted:
// a request an approver has seen keeps its trail (AD-11).
func deletableStatus(statusCode string) bool {
	return statusCode == draftStatusCode || statusCode == "CANC"
}

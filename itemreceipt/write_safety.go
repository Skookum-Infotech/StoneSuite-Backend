package itemreceipt

import (
	"errors"
	"fmt"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this item receipt was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces item receipt creates in request_idempotency.
const idempotencyScopeCreate = "item_receipt.create"

const (
	// maxLineItems caps the lines one receipt may carry. Each line costs
	// several lookups and an insert.
	maxLineItems = 500
	// maxQuantity is the largest magnitude a DECIMAL(14,3) quantity holds.
	maxQuantity = 1e11 - 0.001
	// isoDate is the yyyy-mm-dd layout dates arrive in.
	isoDate = "2006-01-02"
)

// validateWriteInput checks the limits shared by create and update before any
// lookup is done: the line count and the receipt date format.
func validateWriteInput(lineCount int, receiptDate string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("An item receipt can have at most %d lines.", maxLineItems)}
	}
	if receiptDate != "" {
		if _, err := time.Parse(isoDate, receiptDate); err != nil {
			return ClientError{Msg: "receiptDate must be a yyyy-mm-dd date."}
		}
	}
	return nil
}

// validateLineQuantities checks one line's effective quantities. Every
// comparison is in the negated form so NaN is rejected too.
func validateLineQuantities(lineNo int, received, rejected float64) error {
	if !(received > 0 && received <= maxQuantity) {
		return ClientError{Msg: fmt.Sprintf("Line %d: received quantity must be greater than zero, and within range.", lineNo)}
	}
	if !(rejected >= 0) {
		return ClientError{Msg: fmt.Sprintf("Line %d: rejected quantity cannot be negative.", lineNo)}
	}
	if !(rejected <= received) {
		return ClientError{Msg: fmt.Sprintf("Line %d: rejected quantity cannot exceed the received quantity.", lineNo)}
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

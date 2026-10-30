package vendorbill

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this vendor bill was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces vendor bill creates in request_idempotency.
const idempotencyScopeCreate = "vendorbill.create"

// voidStatusCode is the terminal status a voided vendor bill carries.
const voidStatusCode = "VOID"

const (
	// maxLineItems caps the lines one vendor bill may carry. Each line costs
	// several lookups and an insert; an unbounded list is a cheap way to tie
	// up a connection and the request for a long time.
	maxLineItems = 500
	// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column
	// holds. A larger value used to reach Postgres and come back as a 500.
	maxMoneyAmount = 1e13 - 0.01
	// isoDate is the yyyy-mm-dd layout dates arrive in.
	isoDate = "2006-01-02"
	// maxPercent is the upper bound of a percentage field.
	maxPercent = 100
)

// validateWriteInput checks the header- and line-level limits shared by create
// and update. billDate is the effective date (the stored one when an update
// leaves it blank).
func validateWriteInput(items []LineInput, salesTaxPercent, adjustment float64, billDate, dueDate string) error {
	if len(items) > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("A vendor bill can have at most %d lines.", maxLineItems)}
	}
	if !(salesTaxPercent >= 0 && salesTaxPercent <= maxPercent) { // negated form also rejects NaN
		return ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	if !(math.Abs(adjustment) <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "adjustment is out of range."}
	}
	for _, it := range items {
		if !(it.UnitPrice >= 0 && it.UnitPrice <= maxMoneyAmount) {
			return ClientError{Msg: fmt.Sprintf("Line %d: unit price must be zero or more, and within range.", it.LineNumber)}
		}
		if !(it.Quantity >= 0 && it.Quantity <= maxMoneyAmount) {
			return ClientError{Msg: fmt.Sprintf("Line %d: quantity is out of range.", it.LineNumber)}
		}
		if !(it.DiscountPercent >= 0 && it.DiscountPercent <= maxPercent) {
			return ClientError{Msg: fmt.Sprintf("Line %d: discountPercent must be between 0 and 100.", it.LineNumber)}
		}
	}
	return validateDueDate(billDate, dueDate)
}

// validateDueDate rejects a due date before the bill date. Blank or
// unparseable dates are left to the database's own date handling.
func validateDueDate(billDate, dueDate string) error {
	if billDate == "" || dueDate == "" {
		return nil
	}
	bill, err1 := time.Parse(isoDate, billDate)
	due, err2 := time.Parse(isoDate, dueDate)
	if err1 != nil || err2 != nil {
		return nil
	}
	if due.Before(bill) {
		return ClientError{Msg: "dueDate cannot be before billDate."}
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

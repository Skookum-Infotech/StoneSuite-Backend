package purchaseorder

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this purchase order was changed by someone else; reload it and try again")

// ErrHasDependents is returned when a delete is refused because the order has
// moved on: a live item receipt or vendor bill still points at it.
var ErrHasDependents = errors.New("this purchase order cannot be deleted")

// idempotencyScopeCreate namespaces purchase order creates in request_idempotency.
const idempotencyScopeCreate = "purchase_order.create"

const (
	// maxLineItems caps the lines one order may carry. Each line costs several
	// lookups and an insert; an unbounded list is a cheap way to tie up a
	// connection and the request for a long time.
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
// update. orderDate is the effective date (the stored one when an update
// leaves it blank).
func validateWriteInput(lineCount int, shipping, adjustment, salesTaxPercent float64, orderDate, expectedDate string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("A purchase order can have at most %d lines.", maxLineItems)}
	}
	if !(shipping >= 0 && shipping <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "shippingCharge must be zero or more, and within range."}
	}
	if !(math.Abs(adjustment) <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "adjustment is out of range."}
	}
	if !(salesTaxPercent >= 0 && salesTaxPercent <= maxPercent) {
		return ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	return validateExpectedDate(orderDate, expectedDate)
}

// validateExpectedDate rejects an expected date before the order date. Blank
// or unparseable dates are left to the database's own date handling.
func validateExpectedDate(orderDate, expectedDate string) error {
	if orderDate == "" || expectedDate == "" {
		return nil
	}
	ord, err1 := time.Parse(isoDate, orderDate)
	exp, err2 := time.Parse(isoDate, expectedDate)
	if err1 != nil || err2 != nil {
		return nil
	}
	if exp.Before(ord) {
		return ClientError{Msg: "expectedDate cannot be before orderDate."}
	}
	return nil
}

// validateLineAmounts checks one line's typed numbers. Every comparison is in
// the negated form so NaN is rejected too. quantity is the effective quantity
// (already derived from dimensions when the line is sized).
func validateLineAmounts(lineNo int, quantity, unitPrice, discountPercent float64) error {
	if !(quantity > 0 && quantity <= maxQuantity) {
		return ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero, and within range.", lineNo)}
	}
	if !(unitPrice >= 0 && unitPrice <= maxMoneyAmount) {
		return ClientError{Msg: fmt.Sprintf("Line %d: unit price must be zero or more, and within range.", lineNo)}
	}
	if !(discountPercent >= 0 && discountPercent <= maxPercent) {
		return ClientError{Msg: fmt.Sprintf("Line %d: discount must be between 0 and 100.", lineNo)}
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

// effectiveOrderDate is the date a save will store: a blank order date means
// today (see orNow), so that is what the expected date is compared against.
func effectiveOrderDate(d string) string {
	if d == "" || d == "now" {
		return time.Now().Format(isoDate)
	}
	return d
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

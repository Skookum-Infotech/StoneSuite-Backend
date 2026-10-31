package estimate

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this estimate was changed by someone else; reload it and try again")

// ErrConverted is returned when a delete targets an estimate that has already
// been converted: deleting it would orphan the quote made from it.
var ErrConverted = errors.New("a converted estimate cannot be deleted")

// idempotencyScopeCreate namespaces estimate creates in request_idempotency.
const idempotencyScopeCreate = "estimate.create"

const (
	// maxLineItems caps the lines one estimate may carry. Each line costs
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
// update. estimateDate is the effective date (today when the caller left it blank).
func validateWriteInput(lineCount int, shipping, adjustment float64, estimateDate, validUntil string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("An estimate can have at most %d lines.", maxLineItems)}
	}
	if !(shipping >= 0 && shipping <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "shippingCharge must be zero or more, and within range."}
	}
	if !(math.Abs(adjustment) <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "adjustment is out of range."}
	}
	return validateValidUntil(estimateDate, validUntil)
}

// effectiveDate is the estimate date a write will store: the given date, or today
// when blank.
func effectiveDate(d string) string {
	if d == "" {
		return time.Now().Format(isoDate)
	}
	return d
}

// validateValidUntil rejects an expiry before the estimate date. Blank or
// unparseable dates are left to the database's own date handling.
func validateValidUntil(estimateDate, validUntil string) error {
	if estimateDate == "" || validUntil == "" {
		return nil
	}
	qd, err1 := time.Parse(isoDate, estimateDate)
	vu, err2 := time.Parse(isoDate, validUntil)
	if err1 != nil || err2 != nil {
		return nil
	}
	if vu.Before(qd) {
		return ClientError{Msg: "validUntil cannot be before estimateDate."}
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

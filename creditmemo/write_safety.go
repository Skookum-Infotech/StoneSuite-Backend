package creditmemo

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this credit memo was changed by someone else; reload it and try again")

const (
	// idempotencyScopeCreate namespaces credit memo creates in request_idempotency.
	idempotencyScopeCreate = "creditmemo.create"
	// idempotencyScopeApply namespaces credit memo applies in request_idempotency.
	idempotencyScopeApply = "creditmemo.apply"
)

const (
	// maxLineItems caps the lines one credit memo may carry. Each line costs
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
// update. amount is 0 when the memo carries lines (or an update omits it).
func validateWriteInput(lineCount int, amount, adjustment float64, creditMemoDate string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("A credit memo can have at most %d lines.", maxLineItems)}
	}
	if !(amount >= 0 && amount <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "amount must be positive and within range."}
	}
	if !(math.Abs(adjustment) <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "adjustment is out of range."}
	}
	return validateMemoDate(creditMemoDate)
}

// validateMemoDate rejects a non-blank date that is not a valid yyyy-mm-dd.
func validateMemoDate(creditMemoDate string) error {
	if creditMemoDate == "" {
		return nil
	}
	if _, err := time.Parse(isoDate, creditMemoDate); err != nil {
		return ClientError{Msg: "creditMemoDate must be a valid yyyy-mm-dd date."}
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

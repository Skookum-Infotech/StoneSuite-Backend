package vendorpayment

import (
	"errors"
	"fmt"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this vendor payment was changed by someone else; reload it and try again")

// idempotencyScopeCreate namespaces vendor payment creates in request_idempotency.
const idempotencyScopeCreate = "vendorpayment.create"

// idempotencyScopeApply namespaces vendor payment applies in request_idempotency.
const idempotencyScopeApply = "vendorpayment.apply"

const (
	// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column
	// holds. A larger value used to reach Postgres and come back as a 500.
	maxMoneyAmount = 1e13 - 0.01
	// maxInlineApplications caps the bills one create may apply to; each costs
	// two row locks and a rollup inside the create's transaction.
	maxInlineApplications = 100
)

// validateAmount checks a money amount is positive and within range. The
// negated form also rejects NaN.
func validateAmount(field string, amount float64) error {
	if !(amount > 0 && amount <= maxMoneyAmount) {
		return ClientError{Msg: fmt.Sprintf("%s must be positive and within range.", field)}
	}
	return nil
}

// validateCreateAmounts checks the payment amount and every inline
// application amount, and bounds the application count.
func validateCreateAmounts(amount float64, apps []ApplicationInput) error {
	if err := validateAmount("amount", amount); err != nil {
		return err
	}
	if len(apps) > maxInlineApplications {
		return ClientError{Msg: fmt.Sprintf("A vendor payment can apply to at most %d bills at creation.", maxInlineApplications)}
	}
	for _, app := range apps {
		if err := validateAmount("application amount", app.Amount); err != nil {
			return err
		}
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

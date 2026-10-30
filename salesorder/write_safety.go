package salesorder

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrVersionConflict is returned when an update carries an expectedVersion
// that no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this sales order was changed by someone else; reload it and try again")

// ErrHasDependents is returned when a delete is refused because the order has
// moved on: it is partly or fully fulfilled, or a live invoice, fabrication
// job, credit memo or child order still points at it.
var ErrHasDependents = errors.New("this sales order cannot be deleted")

// idempotencyScopeCreate namespaces sales order creates in request_idempotency.
const idempotencyScopeCreate = "sales_order.create"

const (
	// maxLineItems caps the lines one order may carry. Each line costs several
	// lookups, an insert and a stock reservation; an unbounded list is a cheap
	// way to tie up a connection and the request for a long time.
	maxLineItems = 500
	// maxMoneyAmount is the largest magnitude a DECIMAL(15,2) money column
	// holds. A larger value used to reach Postgres and come back as a 500.
	maxMoneyAmount = 1e13 - 0.01
	// isoDate is the yyyy-mm-dd layout dates arrive in.
	isoDate = "2006-01-02"
)

// validateWriteInput checks the header-level limits shared by create and
// update. orderDate is the effective date (the stored one when an update
// leaves it blank).
func validateWriteInput(lineCount int, shipping, adjustment float64, orderDate, expectedDelivery string) error {
	if lineCount > maxLineItems {
		return ClientError{Msg: fmt.Sprintf("A sales order can have at most %d lines.", maxLineItems)}
	}
	if !(shipping >= 0 && shipping <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "shippingCharge must be zero or more, and within range."}
	}
	if !(math.Abs(adjustment) <= maxMoneyAmount) { // negated form also rejects NaN
		return ClientError{Msg: "adjustment is out of range."}
	}
	return validateExpectedDelivery(orderDate, expectedDelivery)
}

// validateExpectedDelivery rejects an expected delivery before the order date.
// Blank or unparseable dates are left to the database's own date handling.
func validateExpectedDelivery(orderDate, expectedDelivery string) error {
	if orderDate == "" || expectedDelivery == "" {
		return nil
	}
	ord, err1 := time.Parse(isoDate, orderDate)
	del, err2 := time.Parse(isoDate, expectedDelivery)
	if err1 != nil || err2 != nil {
		return nil
	}
	if del.Before(ord) {
		return ClientError{Msg: "expectedDelivery cannot be before orderDate."}
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

// deletableStatus reports whether an order in statusCode may still be deleted.
// Once fulfillment has started (PART) or finished (FILL) the order is a record
// of stone that left the yard and must stay.
func deletableStatus(statusCode string) bool {
	return statusCode != "PART" && statusCode != "FILL"
}

// effectiveOrderDate is the date a save will store: a blank order date means
// today (see orNow), so that is what delivery is compared against.
func effectiveOrderDate(d string) string {
	d = strings.TrimSpace(d)
	if d == "" || d == "now" {
		return time.Now().Format(isoDate)
	}
	return d
}

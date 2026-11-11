package vendorpayment

import (
	"errors"
	"sort"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid vendor payment status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7's vendor payment map). A payment created on its own starts at DRFT;
// one recorded against a vendor bill starts at PINI ("Payment Initiated"),
// which otherwise behaves exactly like DRFT. The manual PAPV->APPV edge is
// allowed here, but the guard requiring it go through Approve() instead of a
// bare Transition lives in store_transition.go, added alongside this core.
//
// SENT still means "dispatched" exactly as before (the scheduler's daily tick
// moves a due SCHD payment there — scheduler.go — with no effect on any
// bill). PAID is new and separate: it is "Complete Payment", the step that
// actually settles the bills (and a fully-billed purchase order) a payment is
// applied to. It is reachable straight from Approved or Scheduled too, for a
// payment that skips the dispatch bookkeeping entirely, not only from Sent.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "VOID": true},
	"PINI": {"PAPV": true, "VOID": true},
	"PAPV": {"APPV": true, "DRFT": true, "VOID": true},
	"APPV": {"SCHD": true, "SENT": true, "PAID": true, "VOID": true},
	"SCHD": {"SENT": true, "PAID": true, "VOID": true},
	"SENT": {"PAID": true, "VOID": true},
	"PAID": {"VOID": true},
	"VOID": {},
}

// PaidStatusCode is the status a vendor payment holds once the payment has
// actually been made — what "Complete Payment" moves it to.
const PaidStatusCode = "PAID"

// PaymentInitiatedStatusCode is the status a vendor payment starts at when it
// was recorded against a vendor bill: the money is committed but not yet out.
const PaymentInitiatedStatusCode = "PINI"

// CanTransition reports whether moving fromCode->toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// NextStatuses lists the codes reachable from fromCode in the static map,
// sorted -- the shape approvalchain.NextStatusCodes consumes.
func NextStatuses(fromCode string) []string {
	out := make([]string, 0, len(allowedTransitions[fromCode]))
	for code := range allowedTransitions[fromCode] {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}

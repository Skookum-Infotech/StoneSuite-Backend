package vendorpayment

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid vendor payment status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7's vendor payment map). New payments start at DRFT. The manual
// PAPV->APPV edge is allowed here, but the guard requiring it go through
// Approve() instead of a bare Transition lives in store_transition.go, added
// alongside this core.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "VOID": true},
	"PAPV": {"APPV": true, "DRFT": true, "VOID": true},
	"APPV": {"SCHD": true, "SENT": true, "VOID": true},
	"SCHD": {"SENT": true, "VOID": true},
	"SENT": {"VOID": true},
	"VOID": {},
}

// CanTransition reports whether moving fromCode->toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}

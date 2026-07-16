package payment

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid payment status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7). Terminal states (DEPO, VOID) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	"PEND": {"APPV": true, "VOID": true},
	"APPV": {"DEPO": true, "VOID": true},
	"DEPO": {},
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

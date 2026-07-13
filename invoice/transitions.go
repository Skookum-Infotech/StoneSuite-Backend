package invoice

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid invoice status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7). Terminal states (PAID, VOID) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "VOID": true},
	"PAPV": {"APPV": true, "DRFT": true, "VOID": true},
	"APPV": {"SENT": true, "VOID": true},
	"SENT": {"PART": true, "PAID": true, "ODUE": true, "VOID": true},
	"PART": {"PAID": true, "ODUE": true, "VOID": true},
	"ODUE": {"PART": true, "PAID": true, "VOID": true},
	"PAID": {},
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

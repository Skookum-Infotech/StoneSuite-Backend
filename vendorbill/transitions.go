package vendorbill

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid vendor bill status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7's vendor bill map, AD-15). PART/PAID are reached only by
// DeriveStatus, never through a direct user transition.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "VOID": true},
	"PAPV": {"APPV": true, "DRFT": true, "VOID": true},
	"APPV": {"VOID": true},
	"PART": {"VOID": true},
	"PAID": {"VOID": true},
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

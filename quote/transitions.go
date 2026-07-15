package quote

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid quote status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7). Terminal states (RJCT, EXPR, CANC) map to an empty set. There is
// no "Accepted" status: acceptance is expressed by converting the quote
// into a quote (spec §9.1), which does not require a status change here.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"SENT": true, "CANC": true},
	"SENT": {"RJCT": true, "EXPR": true, "CANC": true},
	"RJCT": {},
	"EXPR": {},
	"CANC": {},
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

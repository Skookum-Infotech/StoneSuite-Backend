package requisition

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid requisition status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (AD-6). Terminal state (CANC) maps to an empty set. There is no RJCT status
// seeded for REQN (same as PORD) — rework is expressed as PAPV -> DRFT
// (recall) or APPV -> DRFT (revise). Mirrors purchaseorder's first three
// states (it has no SENT/PART/RCVD/CLSD — those are PO-only, post-conversion
// concerns).
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"DRFT": true, "CANC": true},
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

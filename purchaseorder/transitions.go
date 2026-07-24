package purchaseorder

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid purchase order status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec AD-5). Terminal states (CLSD, CANC) map to an empty set. There is no
// RJCT status seeded for PORD — rework/rejection is expressed as PAPV → DRFT
// (recall) or APPV → DRFT (revise). CANC is only reachable before any
// receiving; once goods have arrived (PART), the order can only be
// short-closed (→ CLSD), never cancelled.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"SENT": true, "DRFT": true, "CANC": true},
	"SENT": {"PART": true, "RCVD": true, "CLSD": true, "CANC": true},
	"PART": {"RCVD": true, "CLSD": true},
	"RCVD": {"CLSD": true},
	"CLSD": {},
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

package refund

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid refund status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7). Reuses the seeded RFND lifecycle (record_type 10): PEND, APPV,
// SENT, VOID. Terminal states (SENT, VOID) map to an empty set. VOID is
// reachable only from PEND/APPV — once SENT (money physically returned),
// reversing it is a new refund's problem, not a void.
var allowedTransitions = map[string]map[string]bool{
	"PEND": {"APPV": true, "VOID": true},
	"APPV": {"SENT": true, "VOID": true},
	"SENT": {},
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

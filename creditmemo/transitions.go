package creditmemo

import "errors"

// ErrInvalidTransition is returned when a status move is not in the allowed
// map. Maps to HTTP 409.
var ErrInvalidTransition = errors.New("invalid credit memo status transition")

// allowedTransitions is the user-directed status map for the CRDT lifecycle
// (statuses seeded in lkp_record_status for record_type 9).
//
//	DRFT --approve--> APPV --(derived)--> APPL
//	  |                 |  <--(derived)--/
//	  \--> VOID <-------/
//
// Two moves are deliberate:
//
//   - APPV->APPL is derived by the apply path, not user-directed: a memo becomes
//     Applied when its unapplied balance reaches zero, and drops back to APPV on
//     unapply. It is in the map because Apply validates through it.
//   - APPL->VOID is absent. A consumed credit must be unapplied first, which
//     returns it to APPV. This keeps the void cascade bounded and makes "this
//     credit was spent" a real terminal state.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"APPV": true, "VOID": true},
	"APPV": {"APPL": true, "VOID": true},
	"APPL": {},
	"VOID": {},
}

// CanTransition reports whether fromCode may move to toCode.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// ValidateTransition returns ErrInvalidTransition unless the move is allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}

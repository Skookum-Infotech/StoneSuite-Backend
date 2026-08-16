package vendorcredit

import "errors"

// ErrInvalidTransition is returned when a status move is not in the allowed
// map. Maps to HTTP 409.
var ErrInvalidTransition = errors.New("invalid vendor credit status transition")

// allowedTransitions is the user-directed status map for the VCRD lifecycle
// (statuses seeded in lkp_record_status for record_type 17, the exact same
// set as CRDT/credit_memo, spec §5).
//
//	DRFT --approve--> APPV --(derived)--> APPL
//	  |                 |  <--(derived)--/
//	  \--> VOID <-------/
//
// Two moves are deliberate:
//
//   - APPV->APPL is derived by the apply path, not user-directed: a credit
//     becomes Applied when its unapplied balance reaches zero, and drops back
//     to APPV the instant any of it is reversed. It is in the map because
//     Apply validates through it.
//   - APPL->VOID is absent. An exhausted credit must be reversed first, which
//     returns it to APPV. This keeps the void cascade bounded and makes "this
//     credit was spent" a real terminal state (AD-5).
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

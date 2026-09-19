package expense

import (
	"errors"
	"sort"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid expense status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// via the generic Transition path. RJCT is deliberately absent from SUBM's
// set -- rejection is a dedicated decision (Reject, approval.go) that always
// captures a reason and never requires quorum, unlike a generic transition
// (spec AD-5). Terminal state (REIM) maps to an empty set.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"SUBM": true},
	"SUBM": {"APPV": true, "DRFT": true},
	"APPV": {"REIM": true},
	"RJCT": {"DRFT": true},
	"REIM": {},
}

// CanTransition reports whether moving fromCode->toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// NextStatuses lists the codes reachable from fromCode in the static map,
// sorted -- the shape approvalchain.NextStatusCodes consumes.
func NextStatuses(fromCode string) []string {
	out := make([]string, 0, len(allowedTransitions[fromCode]))
	for code := range allowedTransitions[fromCode] {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}

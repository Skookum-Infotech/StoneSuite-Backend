package vendorbill

import (
	"errors"
	"sort"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid vendor bill status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (AD-5) -- invoice's machine minus SENT, since a bill is received rather
// than sent. Terminal states (PAID, VOID) map to an empty set. PART/PAID are
// normally reached by DeriveStatus (balance.go), not this map; they remain
// here so an operator can correct state manually.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "VOID": true},
	"PAPV": {"APPV": true, "DRFT": true, "VOID": true},
	"APPV": {"PART": true, "PAID": true, "ODUE": true, "VOID": true},
	"PART": {"PAID": true, "ODUE": true, "VOID": true},
	"ODUE": {"PART": true, "PAID": true, "VOID": true},
	"PAID": {},
	"VOID": {},
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

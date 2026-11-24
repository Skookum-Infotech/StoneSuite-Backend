package employee

import (
	"errors"
	"sort"
)

// Status codes of the EMPL record type.
const (
	statusPending  = "PEND"
	statusActive   = "ACTV"
	statusInactive = "INAC"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid employee status transition")

// allowedTransitions maps a status code to the codes reachable from it by a
// manual move. Pending -> Active is further gated by the approval chain (see
// Transition); the approvers' own sign-off moves it there through Approve.
var allowedTransitions = map[string]map[string]bool{
	statusPending:  {statusActive: true, statusInactive: true},
	statusActive:   {statusInactive: true},
	statusInactive: {statusActive: true},
}

// CanTransition reports whether moving fromCode -> toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// NextStatuses lists the codes reachable from fromCode, sorted.
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

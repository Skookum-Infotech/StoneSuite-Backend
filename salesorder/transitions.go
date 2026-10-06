package salesorder

import (
	"errors"
	"sort"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid sales order status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §8). Terminal states (FILL, CANC) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"OPEN": true, "CANC": true},
	"OPEN": {"PART": true, "FILL": true, "CANC": true},
	"PART": {"FILL": true, "CANC": true},
	"FILL": {},
	"CANC": {},
}

// convertibleStatuses are the statuses from which downstream documents (an
// invoice, a fabrication job) may be created from an order: it has to be
// approved and confirmed, or further along fulfillment. Draft, Pending
// Approval and Cancelled orders are not a commitment to bill or build.
var convertibleStatuses = map[string]bool{"APPV": true, "OPEN": true, "PART": true, "FILL": true}

// IsConvertible reports whether an order in statusCode may spawn an invoice or
// a fabrication job. The UI hides those actions otherwise; this is the server
// side of the same rule.
func IsConvertible(statusCode string) bool { return convertibleStatuses[statusCode] }

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

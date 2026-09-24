package purchaseorder

import (
	"errors"
	"sort"
)

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

// nonAdminTargets is the set of target statuses a caller who is not a super
// admin may request through the transition endpoint: the two forward moves
// the UI offers as buttons to every purchase_order:transition holder (submit
// for approval, send to vendor). Every other move -- cancel, recall/revise to
// draft, manual receiving, short-close, close -- is a super-admin override.
// Approve and Reject have their own endpoints and are not gated here; receipt
// postings roll the status up internally (ApplyReceiptRollup), not through a
// person's transition request.
var nonAdminTargets = map[string]bool{"PAPV": true, "SENT": true}

// NonAdminMayTransitionTo reports whether a caller who is not a super admin
// may request a move to toCode. It checks the requested target only -- the
// move's legality from the current status is still ValidateTransition's job.
func NonAdminMayTransitionTo(toCode string) bool {
	return nonAdminTargets[toCode]
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}

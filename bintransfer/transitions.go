package bintransfer

import (
	"errors"
	"sort"
)

// Status codes, seeded for record type BTRF.
const (
	StatusDraft     = "DRFT"
	StatusPending   = "PAPV" // pending approval -- the approval gate
	StatusApproved  = "APPV"
	StatusCompleted = "COMP"
	StatusCancelled = "CANC"
)

// recordTypeCode is the lkp_record_type code for Bin Transfer.
const recordTypeCode = "BTRF"

// ErrInvalidTransition is returned when a status move is not in the map. Maps to
// HTTP 409.
var ErrInvalidTransition = errors.New("invalid bin transfer status transition")

// allowedTransitions is the user-directed status graph.
//
//	DRFT -> PAPV -> APPV -> COMP
//	  |       |       |
//	  +-------+-------+--> CANC
//
// PAPV and APPV both go back to DRFT: Reject sends a pending transfer back, and
// Revise reopens an approved one for editing (which restarts approval).
//
// COMP is terminal and CANC is unreachable once it is reached: completing moved
// real stone, and the only honest way back is a new transfer in the other
// direction.
var allowedTransitions = map[string]map[string]bool{
	StatusDraft:     {StatusPending: true, StatusCancelled: true},
	StatusPending:   {StatusApproved: true, StatusDraft: true, StatusCancelled: true},
	StatusApproved:  {StatusCompleted: true, StatusDraft: true, StatusCancelled: true},
	StatusCompleted: {},
	StatusCancelled: {},
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

// IsEditable reports whether the header and lines may still change. Approval
// freezes the document: the moves that were signed off must be the ones applied.
func IsEditable(statusCode string) bool { return statusCode == StatusDraft }

// IsOpen reports whether a transfer in this status still intends to move stone,
// and so holds a claim on its slabs. A slab can be on at most one open transfer.
func IsOpen(statusCode string) bool {
	return statusCode == StatusDraft || statusCode == StatusPending || statusCode == StatusApproved
}

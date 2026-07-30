package cashtransfer

import "errors"

// Record type + status code constants (spec §3's CTRF seed).
const (
	recordTypeCode      = "CTRF"
	draftStatusCode     = "DRFT"
	approvedStatusCode  = "APPR"
	postedStatusCode    = "POST"
	cancelledStatusCode = "CANC"
	reversedStatusCode  = "RVSD"

	// sourceType is the journal_entry.source_type value this module writes
	// (spec AD-2).
	sourceType = "cash_transfer"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid cash transfer status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec AD-5). Terminal states (CANC, RVSD) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	draftStatusCode:     {approvedStatusCode: true, cancelledStatusCode: true},
	approvedStatusCode:  {postedStatusCode: true, cancelledStatusCode: true},
	postedStatusCode:    {reversedStatusCode: true},
	cancelledStatusCode: {},
	reversedStatusCode:  {},
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

// IsPosted reports whether a status represents a transfer that has already
// moved money through the ledger — true for POST and RVSD (a reversed
// transfer is still "posted" in the sense that it can never be posted
// again). Mirrors itemreceipt.IsPosted's role guarding against double-post.
func IsPosted(code string) bool {
	return code == postedStatusCode || code == reversedStatusCode
}

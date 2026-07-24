package itemreceipt

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid item receipt status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (AD-1). The four codes are the ones lkp_record_status already seeds for
// record_type 14 — this module adopts them verbatim rather than seeding a
// Draft/Inspection vocabulary of its own.
//
//	PEND — created, not yet posted. The editable state.
//	PART — posted; the source PO line is not yet fully satisfied.
//	RCVD — posted; the source PO line is fully satisfied.
//	VOID — reversed. Terminal: stock and qty_received have been given back.
//
// PEND → PART/RCVD is the posting move and is never made by a bare transition
// request — Post decides which of the two applies from the quantities. It is
// listed here so Post's own status write validates through the same map.
var allowedTransitions = map[string]map[string]bool{
	"PEND": {"PART": true, "RCVD": true, "VOID": true},
	"PART": {"VOID": true},
	"RCVD": {"VOID": true},
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

// IsPosted reports whether a status code means the receipt's quantities have
// already moved (and so the document is immutable — AD-5).
func IsPosted(code string) bool {
	return code == receivedStatusCode || code == partialStatusCode
}

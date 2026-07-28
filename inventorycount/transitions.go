package inventorycount

import "stonesuite-backend/docflow"

// Status codes, seeded for record type ICNT and adopted verbatim. RVW_ carries
// the trailing-underscore padding the lookup table already uses for 4-character
// codes.
const (
	StatusDraft     = "DRFT"
	StatusCounting  = "CNTG"
	StatusInReview  = "RVW_"
	StatusApproved  = "APPV"
	StatusPosted    = "POST"
	StatusCancelled = "CANC"
)

const recordTypeCode = "ICNT"

// machine is the document's status graph.
//
// CNTG -> DRFT is deliberately absent. Going back to draft would discard the
// frozen snapshot, and re-freezing later would measure the crew's counts
// against a system quantity taken AFTER they counted — turning every real
// variance into zero. A count that has started is either finished or cancelled.
//
// RVW_ -> CNTG exists for the opposite reason: review finding an obvious
// miscount should send the crew back to recount against the SAME snapshot,
// which is exactly what this edge preserves.
var machine = docflow.Machine{
	StatusDraft:     {StatusCounting: true, StatusCancelled: true},
	StatusCounting:  {StatusInReview: true, StatusCancelled: true},
	StatusInReview:  {StatusApproved: true, StatusCounting: true, StatusCancelled: true},
	StatusApproved:  {StatusPosted: true, StatusInReview: true, StatusCancelled: true},
	StatusPosted:    {},
	StatusCancelled: {},
}

// Machine exposes the status graph for the controller's "what next" read.
func Machine() docflow.Machine { return machine }

// IsEditable reports whether the header may still change. Once frozen, the
// scope is fixed — widening it would add lines with a snapshot taken at a
// different moment from the rest.
func IsEditable(statusCode string) bool { return statusCode == StatusDraft }

// AcceptsCounts reports whether physical counts may still be entered.
func AcceptsCounts(statusCode string) bool { return statusCode == StatusCounting }

// IsFrozen reports whether the count is holding its scope against movement.
func IsFrozen(statusCode string) bool {
	switch statusCode {
	case StatusCounting, StatusInReview, StatusApproved:
		return true
	}
	return false
}

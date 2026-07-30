package inventoryadjustment

import "stonesuite-backend/docflow"

// Status codes, seeded for record type IADJ and adopted verbatim.
const (
	StatusDraft     = "DRFT"
	StatusPending   = "PAPV" // pending approval
	StatusApproved  = "APPV"
	StatusPosted    = "POST"
	StatusCancelled = "CANC"
)

// recordTypeCode is the lkp_record_type code for this document.
const recordTypeCode = "IADJ"

// machine is the document's status graph.
//
// Two edges are worth explaining:
//
//   - PAPV -> DRFT is rejection. Without it an approver's only options are to
//     approve something they disagree with or cancel it outright, so in
//     practice they approve and the control is theatre.
//   - APPV -> POST is the only way stock moves, and POST is terminal.
//     Correcting a posted adjustment means raising the opposite one, which
//     keeps both in the trail; an "unpost" edge would quietly erase the first.
var machine = docflow.Machine{
	StatusDraft:     {StatusPending: true, StatusCancelled: true},
	StatusPending:   {StatusApproved: true, StatusDraft: true, StatusCancelled: true},
	StatusApproved:  {StatusPosted: true, StatusDraft: true, StatusCancelled: true},
	StatusPosted:    {},
	StatusCancelled: {},
}

// Machine exposes the status graph for the controller's "what can I do next"
// read.
func Machine() docflow.Machine { return machine }

// IsEditable reports whether the document's lines and header may still change.
// Once approved, editing would mean the numbers signed off are not the numbers
// that post.
func IsEditable(statusCode string) bool { return statusCode == StatusDraft }

// IsFinal reports whether the document has reached a terminal status.
func IsFinal(statusCode string) bool { return machine.IsTerminal(statusCode) }

package fabrication

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted by the
// static transition map (spec §1).
var ErrInvalidTransition = errors.New("invalid fabrication job status transition")

// Status codes for the fabrication lifecycle (lkp_record_status, type FJOB).
const (
	StatusDraft             = "DRFT" // pre-order
	StatusOrderReceived     = "ORCV" // initial state when spawned from a sales order
	StatusMaterialAllocated = "MALC" // slabs reserved
	StatusTemplating        = "TMPL"
	StatusTemplateApproved  = "TAPV" // approval gate #1
	StatusFabricationReady  = "FRDY"
	StatusCutting           = "CUTG" // slabs consumed, stock deducted
	StatusEdging            = "EDGP"
	StatusQCPending         = "QCPD"
	StatusQCPassed          = "QCPS" // approval gate #2
	StatusReadyForShipping  = "RSHP"
	StatusInTransit         = "TRAN"
	StatusInstalling        = "INST"
	StatusCompleted         = "COMP" // terminal
	StatusOnHold            = "HOLD" // resumable
	StatusCancelled         = "CANC" // terminal
)

// allowedTransitions maps a status code to the set of codes reachable from it
// via the PUT .../fabrication/status endpoint. Resume is NOT in this map — it is
// validated separately (see ResumeTarget) because its target is stored on the
// row, not a static edge (spec §1.2). The happy path is linear; QCPD→EDGP is the
// only backward edge in the map (rework); HOLD and CANC are reachable from every
// non-terminal state; COMP and CANC are terminal (empty sets).
var allowedTransitions = map[string]map[string]bool{
	StatusDraft:             {StatusOrderReceived: true, StatusOnHold: true, StatusCancelled: true},
	StatusOrderReceived:     {StatusMaterialAllocated: true, StatusOnHold: true, StatusCancelled: true},
	StatusMaterialAllocated: {StatusTemplating: true, StatusOnHold: true, StatusCancelled: true},
	StatusTemplating:        {StatusTemplateApproved: true, StatusOnHold: true, StatusCancelled: true},
	StatusTemplateApproved:  {StatusFabricationReady: true, StatusOnHold: true, StatusCancelled: true},
	StatusFabricationReady:  {StatusCutting: true, StatusOnHold: true, StatusCancelled: true},
	StatusCutting:           {StatusEdging: true, StatusOnHold: true, StatusCancelled: true},
	StatusEdging:            {StatusQCPending: true, StatusOnHold: true, StatusCancelled: true},
	StatusQCPending:         {StatusQCPassed: true, StatusEdging: true, StatusOnHold: true, StatusCancelled: true}, // QCPD→EDGP rework
	StatusQCPassed:          {StatusReadyForShipping: true, StatusOnHold: true, StatusCancelled: true},
	StatusReadyForShipping:  {StatusInTransit: true, StatusOnHold: true, StatusCancelled: true},
	StatusInTransit:         {StatusInstalling: true, StatusOnHold: true, StatusCancelled: true},
	StatusInstalling:        {StatusCompleted: true, StatusOnHold: true, StatusCancelled: true},
	StatusOnHold:            {StatusCancelled: true}, // resume is not a map edge; only cancel is
	StatusCompleted:         {},
	StatusCancelled:         {},
}

// nonTerminalStatuses is every status a job can be on-hold-from or cancelled
// from — all 16 minus the two terminal states minus HOLD itself.
var nonTerminalStatuses = []string{
	StatusDraft, StatusOrderReceived, StatusMaterialAllocated, StatusTemplating,
	StatusTemplateApproved, StatusFabricationReady, StatusCutting, StatusEdging,
	StatusQCPending, StatusQCPassed, StatusReadyForShipping, StatusInTransit,
	StatusInstalling,
}

// CanTransition reports whether moving fromCode→toCode is allowed by the static
// map. It deliberately does NOT cover resume (HOLD→prior status) — see
// CanHold / the resume path in store_transition.go.
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

// IsTerminal reports whether a status has no outbound edges.
func IsTerminal(code string) bool {
	return code == StatusCompleted || code == StatusCancelled
}

// deductsAtOrAfter reports whether a status is at or past CUTG in the linear
// happy path — i.e. the job's slabs have been consumed and stock deducted, so
// they can no longer simply be released (spec §4.4).
func deductsAtOrAfter(code string) bool {
	switch code {
	case StatusCutting, StatusEdging, StatusQCPending, StatusQCPassed,
		StatusReadyForShipping, StatusInTransit, StatusInstalling, StatusCompleted:
		return true
	}
	return false
}

package fabrication

import "testing"

// allStatuses is every FJOB status code, for exhaustive matrix testing.
var allStatuses = []string{
	StatusDraft, StatusOrderReceived, StatusMaterialAllocated, StatusTemplating,
	StatusTemplateApproved, StatusFabricationReady, StatusCutting, StatusEdging,
	StatusQCPending, StatusQCPassed, StatusReadyForShipping, StatusInTransit,
	StatusInstalling, StatusCompleted, StatusOnHold, StatusCancelled,
}

func TestCanTransitionHappyPath(t *testing.T) {
	happy := [][2]string{
		{StatusDraft, StatusOrderReceived},
		{StatusOrderReceived, StatusMaterialAllocated},
		{StatusMaterialAllocated, StatusTemplating},
		{StatusTemplating, StatusTemplateApproved},
		{StatusTemplateApproved, StatusFabricationReady},
		{StatusFabricationReady, StatusCutting},
		{StatusCutting, StatusEdging},
		{StatusEdging, StatusQCPending},
		{StatusQCPending, StatusQCPassed},
		{StatusQCPassed, StatusReadyForShipping},
		{StatusReadyForShipping, StatusInTransit},
		{StatusInTransit, StatusInstalling},
		{StatusInstalling, StatusCompleted},
	}
	for _, p := range happy {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("expected happy-path edge %s->%s allowed", p[0], p[1])
		}
	}
}

func TestReworkEdge(t *testing.T) {
	if !CanTransition(StatusQCPending, StatusEdging) {
		t.Error("QCPD->EDGP rework edge must be allowed")
	}
	// Skipping the QC gate is not allowed.
	if CanTransition(StatusQCPending, StatusReadyForShipping) {
		t.Error("QCPD->RSHP must be rejected (cannot skip the QCPS gate)")
	}
}

func TestTerminalStatesHaveNoOutboundEdges(t *testing.T) {
	for _, term := range []string{StatusCompleted, StatusCancelled} {
		if !IsTerminal(term) {
			t.Errorf("%s should be terminal", term)
		}
		for _, to := range allStatuses {
			if CanTransition(term, to) {
				t.Errorf("terminal %s must have no edge, found %s->%s", term, term, to)
			}
		}
	}
}

func TestHoldReachableFromEveryNonTerminalButNotItself(t *testing.T) {
	// HOLD reachable from all 13 non-terminal, non-HOLD states.
	if len(nonTerminalStatuses) != 13 {
		t.Fatalf("expected 13 non-terminal source states, got %d", len(nonTerminalStatuses))
	}
	for _, from := range nonTerminalStatuses {
		if !CanTransition(from, StatusOnHold) {
			t.Errorf("HOLD must be reachable from %s", from)
		}
	}
	// HOLD must NOT be reachable from itself — that would overwrite the
	// held-from status and strand the job (spec §1.2).
	if CanTransition(StatusOnHold, StatusOnHold) {
		t.Error("HOLD->HOLD must be rejected")
	}
}

func TestHoldOnlyExitsToCancel(t *testing.T) {
	for _, to := range allStatuses {
		allowed := CanTransition(StatusOnHold, to)
		if to == StatusCancelled {
			if !allowed {
				t.Error("HOLD->CANC must be allowed")
			}
		} else if allowed {
			t.Errorf("HOLD must only exit to CANC via the map, found HOLD->%s (resume is not a map edge)", to)
		}
	}
}

func TestCancelReachableFromEveryNonTerminal(t *testing.T) {
	for _, from := range allStatuses {
		if IsTerminal(from) {
			continue
		}
		if !CanTransition(from, StatusCancelled) {
			t.Errorf("CANC must be reachable from %s", from)
		}
	}
}

func TestFullMatrixNoStrayEdges(t *testing.T) {
	// Every allowed edge must be one we expect: forward-linear, the QCPD->EDGP
	// rework, or ->HOLD / ->CANC. Nothing else.
	linear := map[string]string{
		StatusDraft: StatusOrderReceived, StatusOrderReceived: StatusMaterialAllocated,
		StatusMaterialAllocated: StatusTemplating, StatusTemplating: StatusTemplateApproved,
		StatusTemplateApproved: StatusFabricationReady, StatusFabricationReady: StatusCutting,
		StatusCutting: StatusEdging, StatusEdging: StatusQCPending,
		StatusQCPending: StatusQCPassed, StatusQCPassed: StatusReadyForShipping,
		StatusReadyForShipping: StatusInTransit, StatusInTransit: StatusInstalling,
		StatusInstalling: StatusCompleted,
	}
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			if !CanTransition(from, to) {
				continue
			}
			switch {
			case to == StatusOnHold && from != StatusOnHold && !IsTerminal(from):
			case to == StatusCancelled && !IsTerminal(from):
			case linear[from] == to:
			case from == StatusQCPending && to == StatusEdging: // rework
			default:
				t.Errorf("unexpected allowed edge %s->%s", from, to)
			}
		}
	}
}

func TestValidateTransitionError(t *testing.T) {
	if err := ValidateTransition(StatusDraft, StatusCompleted); err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
	if err := ValidateTransition(StatusDraft, StatusOrderReceived); err != nil {
		t.Errorf("expected nil for valid edge, got %v", err)
	}
}

func TestDeductsAtOrAfterCutting(t *testing.T) {
	deducting := map[string]bool{
		StatusCutting: true, StatusEdging: true, StatusQCPending: true, StatusQCPassed: true,
		StatusReadyForShipping: true, StatusInTransit: true, StatusInstalling: true, StatusCompleted: true,
	}
	for _, s := range allStatuses {
		if got := deductsAtOrAfter(s); got != deducting[s] {
			t.Errorf("deductsAtOrAfter(%s) = %v, want %v", s, got, deducting[s])
		}
	}
}

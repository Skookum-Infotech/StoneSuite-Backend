package creditmemo

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"draft to approved", "DRFT", "APPV", true},
		{"draft to void", "DRFT", "VOID", true},
		{"approved to applied", "APPV", "APPL", true},
		{"approved to void", "APPV", "VOID", true},

		// APPL->VOID is absent by design: a consumed credit must be unapplied
		// first (which returns it to APPV), so the void cascade stays bounded.
		{"applied to void is blocked", "APPL", "VOID", false},
		{"applied is terminal", "APPL", "APPV", false},
		{"void is terminal", "VOID", "DRFT", false},
		{"void to applied", "VOID", "APPL", false},

		{"draft cannot skip to applied", "DRFT", "APPL", false},
		{"approved cannot go back to draft", "APPV", "DRFT", false},
		{"self transition", "DRFT", "DRFT", false},
		{"unknown from", "XXXX", "APPV", false},
		{"unknown to", "DRFT", "XXXX", false},
		{"empty codes", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransition(tc.from, tc.to); got != tc.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "APPV"); err != nil {
		t.Errorf("ValidateTransition(DRFT, APPV) = %v, want nil", err)
	}
	err := ValidateTransition("APPL", "VOID")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("ValidateTransition(APPL, VOID) = %v, want ErrInvalidTransition", err)
	}
}

// The lifecycle is fixed by the lkp_record_status seed for record_type 9, which
// is append-only (statuses key to record types by hardcoded integer). This
// pins the map to those four codes so a stray edit is caught here.
func TestLifecycleMatchesSeededStatuses(t *testing.T) {
	seeded := []string{"DRFT", "APPV", "APPL", "VOID"}
	if len(allowedTransitions) != len(seeded) {
		t.Fatalf("allowedTransitions has %d states, want %d", len(allowedTransitions), len(seeded))
	}
	for _, code := range seeded {
		if _, ok := allowedTransitions[code]; !ok {
			t.Errorf("seeded status %q missing from allowedTransitions", code)
		}
	}
	for from, tos := range allowedTransitions {
		for to := range tos {
			if _, ok := allowedTransitions[to]; !ok {
				t.Errorf("transition %q->%q targets a status that is not seeded", from, to)
			}
		}
	}
}

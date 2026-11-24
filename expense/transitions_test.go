package expense

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "SUBM", true},
		{"DRFT", "APPV", false},
		{"DRFT", "RJCT", false},
		{"SUBM", "APPV", true},
		{"SUBM", "DRFT", true},
		{"SUBM", "RJCT", false}, // reject is a dedicated decision, not a generic transition
		{"APPV", "PAID", false}, // paid only by the vendor bill settling,
		{"APPV", "DRFT", false},
		{"APPV", "RJCT", false},
		{"RJCT", "DRFT", true},
		{"RJCT", "APPV", false},
		{"PAID", "DRFT", false},
		{"UNKNOWN", "DRFT", false},
	}
	for _, tt := range tests {
		if got := CanTransition(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "SUBM"); err != nil {
		t.Errorf("ValidateTransition(DRFT, SUBM) = %v, want nil", err)
	}
	if err := ValidateTransition("PAID", "DRFT"); err != ErrInvalidTransition {
		t.Errorf("ValidateTransition(PAID, DRFT) = %v, want ErrInvalidTransition", err)
	}
}

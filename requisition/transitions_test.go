package requisition

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "PAPV", true},
		{"DRFT", "CANC", true},
		{"DRFT", "APPV", false},
		{"PAPV", "APPV", true},
		{"PAPV", "DRFT", true},
		{"PAPV", "CANC", true},
		{"APPV", "DRFT", true},
		{"APPV", "CANC", true},
		{"APPV", "PAPV", false},
		{"CANC", "DRFT", false},
		{"CANC", "PAPV", false},
		{"UNKNOWN", "DRFT", false},
	}
	for _, tt := range tests {
		if got := CanTransition(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "PAPV"); err != nil {
		t.Errorf("ValidateTransition(DRFT, PAPV) = %v, want nil", err)
	}
	if err := ValidateTransition("CANC", "DRFT"); err != ErrInvalidTransition {
		t.Errorf("ValidateTransition(CANC, DRFT) = %v, want ErrInvalidTransition", err)
	}
}

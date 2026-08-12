package vendorbill

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "PAPV", true},
		{"DRFT", "VOID", true},
		{"DRFT", "APPV", false}, // must pass through PAPV
		{"PAPV", "APPV", true},
		{"PAPV", "DRFT", true}, // recall
		{"APPV", "PART", true},
		{"APPV", "PAID", true},
		{"APPV", "ODUE", true},
		{"APPV", "DRFT", false}, // no recall once approved
		{"PART", "PAID", true},
		{"PART", "ODUE", true},
		{"ODUE", "PART", true},
		{"ODUE", "PAID", true},
		{"PAID", "VOID", false}, // terminal
		{"VOID", "DRFT", false}, // terminal
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
	if err := ValidateTransition("PAID", "VOID"); err != ErrInvalidTransition {
		t.Errorf("ValidateTransition(PAID, VOID) = %v, want ErrInvalidTransition", err)
	}
}

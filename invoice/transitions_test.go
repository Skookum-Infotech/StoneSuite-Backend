package invoice

import "testing"

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "PAPV", true},
		{"DRFT", "VOID", true},
		{"DRFT", "SENT", false},
		{"PAPV", "APPV", true},
		{"PAPV", "DRFT", true},
		{"PAPV", "VOID", true},
		{"APPV", "SENT", true},
		{"APPV", "VOID", true},
		{"SENT", "PART", true},
		{"SENT", "PAID", true},
		{"SENT", "ODUE", true},
		{"SENT", "VOID", true},
		{"PART", "PAID", true},
		{"PART", "ODUE", true},
		{"PART", "VOID", true},
		{"PART", "SENT", false},
		{"ODUE", "PART", true},
		{"ODUE", "PAID", true},
		{"ODUE", "VOID", true},
		{"PAID", "VOID", false},
		{"VOID", "DRFT", false},
	}

	for _, tt := range tests {
		t.Run(tt.from+"->"+tt.to, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
			err := ValidateTransition(tt.from, tt.to)
			if tt.want && err != nil {
				t.Errorf("ValidateTransition(%q, %q) returned error: %v", tt.from, tt.to, err)
			}
			if !tt.want && err == nil {
				t.Errorf("ValidateTransition(%q, %q) expected error, got nil", tt.from, tt.to)
			}
		})
	}
}

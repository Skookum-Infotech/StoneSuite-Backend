package refund

import "testing"

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"PEND", "APPV", true},
		{"PEND", "VOID", true},
		{"PEND", "SENT", false},
		{"APPV", "SENT", true},
		{"APPV", "VOID", true},
		{"APPV", "PEND", false},
		{"SENT", "VOID", false},
		{"SENT", "APPV", false},
		{"VOID", "PEND", false},
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

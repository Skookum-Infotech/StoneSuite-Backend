package vendorpayment

import "testing"

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "PAPV", true},
		{"DRFT", "VOID", true},
		{"DRFT", "APPV", false},
		{"DRFT", "SCHD", false},
		{"PAPV", "APPV", true},
		{"PAPV", "DRFT", true},
		{"PAPV", "VOID", true},
		{"PAPV", "SCHD", false},
		{"PAPV", "SENT", false},
		{"APPV", "SCHD", true},
		{"APPV", "SENT", true},
		{"APPV", "VOID", true},
		{"APPV", "PAPV", false},
		{"APPV", "DRFT", false},
		{"SCHD", "SENT", true},
		{"SCHD", "VOID", true},
		{"SCHD", "APPV", false},
		{"SCHD", "DRFT", false},
		{"SENT", "VOID", true},
		{"SENT", "SCHD", false},
		{"SENT", "APPV", false},
		{"VOID", "DRFT", false},
		{"VOID", "PAPV", false},
		{"VOID", "VOID", false},
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

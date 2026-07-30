package cashtransfer

import "testing"

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "APPR", true},
		{"DRFT", "CANC", true},
		{"DRFT", "POST", false},
		{"DRFT", "RVSD", false},
		{"APPR", "POST", true},
		{"APPR", "CANC", true},
		{"APPR", "DRFT", false},
		{"APPR", "RVSD", false},
		{"POST", "RVSD", true},
		{"POST", "CANC", false},
		{"POST", "APPR", false},
		{"CANC", "DRFT", false},
		{"CANC", "APPR", false},
		{"RVSD", "POST", false},
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

func TestIsPosted(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"DRFT", false}, {"APPR", false}, {"POST", true}, {"CANC", false}, {"RVSD", true},
	}
	for _, tt := range tests {
		if got := IsPosted(tt.code); got != tt.want {
			t.Errorf("IsPosted(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

package purchaseorder

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		want     bool
	}{
		{"draft submits for approval", "DRFT", "PAPV", true},
		{"draft cancels", "DRFT", "CANC", true},
		{"draft cannot skip to sent", "DRFT", "SENT", false},
		{"draft cannot skip to approved", "DRFT", "APPV", false},
		{"pending approves", "PAPV", "APPV", true},
		{"pending recalls to draft", "PAPV", "DRFT", true},
		{"pending cancels", "PAPV", "CANC", true},
		{"approved sends", "APPV", "SENT", true},
		{"approved revises to draft", "APPV", "DRFT", true},
		{"approved cancels", "APPV", "CANC", true},
		{"sent partially receives", "SENT", "PART", true},
		{"sent fully receives", "SENT", "RCVD", true},
		{"sent short-closes", "SENT", "CLSD", true},
		{"sent cancels", "SENT", "CANC", true},
		{"partial fully receives", "PART", "RCVD", true},
		{"partial short-closes", "PART", "CLSD", true},
		{"partial cannot cancel once goods arrived", "PART", "CANC", false},
		{"received closes", "RCVD", "CLSD", true},
		{"received cannot cancel", "RCVD", "CANC", false},
		{"closed is terminal", "CLSD", "DRFT", false},
		{"cancelled is terminal", "CANC", "DRFT", false},
		{"unknown from", "XXXX", "DRFT", false},
		{"unknown to", "DRFT", "XXXX", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "PAPV"); err != nil {
		t.Fatalf("ValidateTransition(DRFT, PAPV) = %v, want nil", err)
	}
	if err := ValidateTransition("CLSD", "DRFT"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition(CLSD, DRFT) = %v, want ErrInvalidTransition", err)
	}
}

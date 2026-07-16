package quote

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	ok := [][2]string{
		{"DRFT", "PAPV"}, {"DRFT", "CANC"},
		{"PAPV", "APPV"}, {"PAPV", "DRFT"}, {"PAPV", "CANC"},
		{"APPV", "SENT"}, {"APPV", "CANC"},
		{"SENT", "RJCT"}, {"SENT", "EXPR"}, {"SENT", "CANC"},
	}
	for _, pair := range ok {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%q, %q) = false, want true", pair[0], pair[1])
		}
	}

	bad := [][2]string{
		{"DRFT", "APPV"}, // must go through PAPV
		{"DRFT", "SENT"}, // can't skip straight to sent
		{"RJCT", "DRFT"}, // terminal
		{"EXPR", "SENT"}, // terminal
		{"CANC", "DRFT"}, // terminal
		{"SENT", "APPV"}, // can't go backward
		{"APPV", "DRFT"}, // no backward path from APPV
	}
	for _, pair := range bad {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "PAPV"); err != nil {
		t.Fatalf("ValidateTransition(DRFT, PAPV) = %v, want nil", err)
	}
	err := ValidateTransition("DRFT", "APPV")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition(DRFT, APPV) = %v, want ErrInvalidTransition", err)
	}
}

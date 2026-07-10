package salesorder

import "testing"

func TestCanTransition(t *testing.T) {
	ok := [][2]string{{"DRFT", "PAPV"}, {"PAPV", "APPV"}, {"APPV", "OPEN"}, {"OPEN", "PART"}, {"OPEN", "FILL"}, {"PART", "FILL"}, {"OPEN", "CANC"}, {"PAPV", "DRFT"}}
	bad := [][2]string{{"FILL", "OPEN"}, {"CANC", "DRFT"}, {"DRFT", "FILL"}, {"APPV", "DRFT"}, {"FILL", "CANC"}}
	for _, p := range ok {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("expected %s->%s allowed", p[0], p[1])
		}
	}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) {
			t.Errorf("expected %s->%s denied", p[0], p[1])
		}
	}
}

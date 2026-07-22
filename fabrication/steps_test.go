package fabrication

import "testing"

func TestCanonicalStepsAreComplete(t *testing.T) {
	if len(canonicalSteps) != 16 {
		t.Fatalf("expected 16 canonical steps, got %d", len(canonicalSteps))
	}
	seenSeq := map[int]bool{}
	seenCode := map[string]bool{}
	for _, s := range canonicalSteps {
		if s.Sequence < 1 || s.Sequence > 16 {
			t.Errorf("step %s has out-of-range sequence %d", s.Code, s.Sequence)
		}
		if seenSeq[s.Sequence] {
			t.Errorf("duplicate sequence %d", s.Sequence)
		}
		if seenCode[s.Code] {
			t.Errorf("duplicate code %s", s.Code)
		}
		if s.Grain != "job" && s.Grain != "piece" {
			t.Errorf("step %s has invalid grain %q", s.Code, s.Grain)
		}
		seenSeq[s.Sequence] = true
		seenCode[s.Code] = true
	}
}

func TestReworkStepsAreCanonical(t *testing.T) {
	codes := map[string]bool{}
	for _, s := range canonicalSteps {
		codes[s.Code] = true
	}
	for _, rc := range reworkStepCodes {
		if !codes[rc] {
			t.Errorf("rework step %s is not a canonical step", rc)
		}
	}
}

func TestValidStepStatus(t *testing.T) {
	valid := []string{StepPending, StepInProgress, StepBlocked, StepSkipped, StepCompleted}
	for _, s := range valid {
		if !validStepStatus(s) {
			t.Errorf("%s should be a valid step status", s)
		}
	}
	if validStepStatus("bogus") {
		t.Error("bogus should not be a valid step status")
	}
}

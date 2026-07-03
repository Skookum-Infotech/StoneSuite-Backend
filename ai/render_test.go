package ai

import (
	"strings"
	"testing"
)

func TestRenderRecord(t *testing.T) {
	doc := RecordDoc{
		WorkflowKey: "prospect",
		StateName:   "In Negotiation",
		Core:        map[string]any{"company_name": "Acme"},
		Custom:      map[string]any{"deal_size": 5000, "priority": "high"},
	}
	out := RenderRecord(doc)

	for _, want := range []string{
		"Workflow: prospect", "State: In Negotiation",
		"company_name: Acme", "deal_size: 5000", "priority: high",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\ngot:\n%s", want, out)
		}
	}
}

func TestRenderRecordIsDeterministic(t *testing.T) {
	// Same input (map order must not matter) -> identical output -> identical hash.
	doc := RecordDoc{
		WorkflowKey: "lead",
		StateName:   "New",
		Custom:      map[string]any{"b": 2, "a": 1, "c": 3},
	}
	if RenderRecord(doc) != RenderRecord(doc) {
		t.Fatal("RenderRecord is not deterministic across calls")
	}
}

func TestContentHashChangesWithContent(t *testing.T) {
	h1 := ContentHash("alpha")
	h2 := ContentHash("beta")
	if h1 == h2 {
		t.Fatal("different content produced the same hash")
	}
	if ContentHash("alpha") != h1 {
		t.Fatal("hash is not stable for identical content")
	}
	if len(h1) != 64 { // sha256 hex
		t.Fatalf("hash len = %d, want 64", len(h1))
	}
}

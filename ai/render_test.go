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

	// Core fields and unlabeled Custom fields both fall back to a humanized
	// (Title Case) form of their raw key — more readable for both embedding
	// recall and a small chat model than the bare snake_case key.
	for _, want := range []string{
		"Workflow: prospect", "State: In Negotiation",
		"Company Name: Acme", "Deal Size: 5000", "Priority: high",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\ngot:\n%s", want, out)
		}
	}
}

// TestRenderRecordUsesAdminLabelOverHumanizedKey confirms a Custom field with
// an admin-defined FieldLabels entry renders under that label, not the
// humanized fallback — the label is the ground truth for what an admin
// actually called the field in their workflow.
func TestRenderRecordUsesAdminLabelOverHumanizedKey(t *testing.T) {
	doc := RecordDoc{
		WorkflowKey: "prospect",
		StateName:   "New",
		Custom:      map[string]any{"deal_size": 5000},
		FieldLabels: map[string]string{"deal_size": "Deal Size ($)"},
	}
	out := RenderRecord(doc)
	if !strings.Contains(out, "Deal Size ($): 5000") {
		t.Errorf("want admin label to win over humanized key, got:\n%s", out)
	}
	if strings.Contains(out, "Deal Size: 5000") {
		t.Errorf("humanized fallback must not also appear once a label exists:\n%s", out)
	}
}

func TestHumanizeKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"company_name", "Company Name"},
		{"companyName", "Company Name"},
		{"priority", "Priority"},
		{"deal_size_usd", "Deal Size Usd"},
	}
	for _, tt := range tests {
		if got := humanizeKey(tt.in); got != tt.want {
			t.Errorf("humanizeKey(%q) = %q, want %q", tt.in, got, tt.want)
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
	first := RenderRecord(doc)
	second := RenderRecord(doc)
	if first != second {
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

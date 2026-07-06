package docs

import "testing"

// TestFSContainsAIAssistantDoc guards against a typo in the go:embed
// pattern silently producing an empty FS (which would make the
// reindex-help endpoint ingest nothing without any error).
func TestFSContainsAIAssistantDoc(t *testing.T) {
	data, err := FS.ReadFile("ai-assistant.md")
	if err != nil {
		t.Fatalf("FS.ReadFile(ai-assistant.md): %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ai-assistant.md embedded as empty")
	}
}

// TestFSContainsCRMConceptsDoc guards the same silent go:embed failure mode
// for crm-concepts.md — the conceptual/meta-question app-help content (what
// is a lead/prospect/customer, what can the assistant do) that closes the gap
// where those questions previously found nothing to retrieve from.
func TestFSContainsCRMConceptsDoc(t *testing.T) {
	data, err := FS.ReadFile("crm-concepts.md")
	if err != nil {
		t.Fatalf("FS.ReadFile(crm-concepts.md): %v", err)
	}
	if len(data) == 0 {
		t.Fatal("crm-concepts.md embedded as empty")
	}
}

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

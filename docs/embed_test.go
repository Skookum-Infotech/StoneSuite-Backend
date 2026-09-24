package docs

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

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

// envTokenPattern flags ALL_CAPS env/config-style identifiers (e.g.
// JWT_SECRET, OLLAMA_BASE_URL) that a user-facing doc has no business
// mentioning.
var envTokenPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b`)

// TestHelpCorpusIsUserFacing guards the bug this package exists to prevent: an
// engineering doc (API paths, table names, internal services, env vars,
// fenced code) shipping as end-user assistant help. It also asserts
// ai-assistant.md — an engineering doc describing the assistant's own
// internals — is deliberately not embedded.
func TestHelpCorpusIsUserFacing(t *testing.T) {
	if _, err := FS.ReadFile("ai-assistant.md"); err == nil {
		t.Fatal("ai-assistant.md is embedded in FS — it is an engineering doc and must not be end-user help")
	}

	err := fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := FS.ReadFile(path)
		if err != nil {
			t.Fatalf("FS.ReadFile(%s): %v", path, err)
		}
		content := string(data)

		if strings.Contains(content, "/api/") {
			t.Errorf("%s: contains \"/api/\" — engineering content in end-user help", path)
		}
		if strings.Contains(content, "_chunks") {
			t.Errorf("%s: contains \"_chunks\" — engineering content in end-user help", path)
		}
		if strings.Contains(content, "rag_") {
			t.Errorf("%s: contains \"rag_\" — engineering content in end-user help", path)
		}
		if strings.Contains(content, "```") {
			t.Errorf("%s: contains a fenced code block — engineering content in end-user help", path)
		}
		for i, line := range strings.Split(content, "\n") {
			if m := envTokenPattern.FindString(line); m != "" {
				t.Errorf("%s:%d: contains env/config-style token %q — engineering content in end-user help: %q", path, i+1, m, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs FS: %v", err)
	}
}

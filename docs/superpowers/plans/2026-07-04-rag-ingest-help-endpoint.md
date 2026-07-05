# rag-ingest-help In-Process Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the SSH+base64+Go-toolchain-install dance for running `rag-ingest-help` against production with a single authenticated HTTP call against the already-deployed backend.

**Architecture:** `docs/*.md` gets compiled into the binary via `//go:embed`. The chunking + ingestion logic moves from `cmd/rag-ingest-help` (a `package main`, not importable) into a new `ai/helpdocs` package. A new platform-admin-gated handler, `AIOps.ReindexHelp`, calls that package's `IngestFS` against the embedded docs. `cmd/rag-ingest-help` keeps working for local dev as a thin wrapper over the same package.

**Tech Stack:** Go 1.25, `embed` (stdlib), `testing/fstest` (stdlib, for tests), existing `ai`/`tenancy`/`middleware`/`controllers` packages.

---

## Global invariants

- `go build ./... && go test ./...` must pass after every task.
- No network calls (Ollama, Postgres) in any test in this plan — use `ai.FakeEmbedder` and in-memory fakes throughout, matching the existing `ai` package test conventions.
- Follow existing codebase conventions: interfaces are defined at the point of use (see `ai/orchestrator.go`'s `Retriever`), not pre-emptively in a shared package.

---

### Task 1: Move `ChunkMarkdown` into a new `ai/helpdocs` package

**Files:**
- Create: `ai/helpdocs/chunk.go`
- Create: `ai/helpdocs/chunk_test.go`

This task only adds the new package — it does not yet touch `cmd/rag-ingest-help`, so the existing CLI keeps working unchanged (with its own copy of `chunk.go`) until Task 4 removes the duplicate.

- [ ] **Step 1: Create the new package file**

`ai/helpdocs/chunk.go`:

```go
// Package helpdocs chunks and ingests app-help markdown docs into the
// control-plane's cp_rag_chunks table. Used by both the rag-ingest-help CLI
// (local dev) and the POST /api/platform/ai/reindex-help handler (prod).
package helpdocs

import (
	"regexp"
	"strings"
)

// Section is one heading-delimited chunk of a markdown document.
type Section struct {
	Title   string
	Content string
}

var headingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)

// ChunkMarkdown splits markdown text into sections at each heading line
// ("#".."######"). Each section's Content starts at its heading line and
// runs to (but not including) the next heading. Content before the first
// heading is dropped, UNLESS the document has no headings at all, in which
// case the whole document becomes one section titled fallbackTitle. An empty
// document produces no sections.
func ChunkMarkdown(text, fallbackTitle string) []Section {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")

	var sections []Section
	var curTitle string
	var curLines []string
	inSection := false

	flush := func() {
		if !inSection {
			return
		}
		content := strings.TrimSpace(strings.Join(curLines, "\n"))
		sections = append(sections, Section{Title: curTitle, Content: content})
	}

	for _, line := range lines {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			curTitle = m[1]
			curLines = []string{line}
			inSection = true
			continue
		}
		if inSection {
			curLines = append(curLines, line)
		}
	}
	flush()

	if sections == nil {
		return []Section{{Title: fallbackTitle, Content: strings.TrimSpace(text)}}
	}
	return sections
}
```

- [ ] **Step 2: Move the existing test file**

`ai/helpdocs/chunk_test.go` (identical to `cmd/rag-ingest-help/chunk_test.go`, just `package helpdocs`):

```go
package helpdocs

import "testing"

func TestChunkMarkdown(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		fallbackTitle string
		want          []Section
	}{
		{
			name: "single heading",
			text: "# Getting Started\nCreate a lead from CRM > Leads > New.\n",
			want: []Section{
				{Title: "Getting Started", Content: "# Getting Started\nCreate a lead from CRM > Leads > New."},
			},
		},
		{
			name: "multiple headings, mixed levels",
			text: "# Onboarding\nWelcome.\n\n## Step 1\nCreate a tenant.\n\n## Step 2\nInvite users.\n",
			want: []Section{
				{Title: "Onboarding", Content: "# Onboarding\nWelcome."},
				{Title: "Step 1", Content: "## Step 1\nCreate a tenant."},
				{Title: "Step 2", Content: "## Step 2\nInvite users."},
			},
		},
		{
			name:          "no headings at all falls back to one section",
			text:          "Just a plain paragraph with no heading.",
			fallbackTitle: "readme",
			want: []Section{
				{Title: "readme", Content: "Just a plain paragraph with no heading."},
			},
		},
		{
			name:          "empty document produces no sections",
			text:          "",
			fallbackTitle: "empty",
			want:          nil,
		},
		{
			name: "content before first heading is dropped",
			text: "Intro paragraph nobody chunks.\n\n# Real Section\nContent.\n",
			want: []Section{
				{Title: "Real Section", Content: "# Real Section\nContent."},
			},
		},
		{
			name: "heading with no body still produces a section",
			text: "# Empty Section\n\n# Next Section\nHas content.\n",
			want: []Section{
				{Title: "Empty Section", Content: "# Empty Section"},
				{Title: "Next Section", Content: "# Next Section\nHas content."},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkMarkdown(tt.text, tt.fallbackTitle)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d sections, want %d\ngot:  %+v\nwant: %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("section %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
```

- [ ] **Step 3: Run the tests**

Run: `go test ./ai/helpdocs/... -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 4: Full build/test check**

Run: `go build ./... && go test ./...`
Expected: PASS (this task only added a new package; nothing else changed yet)

- [ ] **Step 5: Commit**

```bash
git add ai/helpdocs/chunk.go ai/helpdocs/chunk_test.go
git commit -m "feat: add ai/helpdocs package with ChunkMarkdown"
```

---

### Task 2: Add `IngestFS` orchestration to `ai/helpdocs`

**Files:**
- Create: `ai/helpdocs/ingest.go`
- Create: `ai/helpdocs/ingest_test.go`

- [ ] **Step 1: Write the failing test**

`ai/helpdocs/ingest_test.go`:

```go
package helpdocs

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"stonesuite-backend/ai"
)

type fakeHelpStore struct {
	docs   map[string][]ai.HelpChunk
	failOn map[string]error
}

func (f *fakeHelpStore) ReplaceDoc(_ context.Context, docKey string, chunks []ai.HelpChunk) error {
	if err, ok := f.failOn[docKey]; ok {
		return err
	}
	if f.docs == nil {
		f.docs = map[string][]ai.HelpChunk{}
	}
	f.docs[docKey] = chunks
	return nil
}

func TestIngestFS_EmbedsAndReplacesEachDoc(t *testing.T) {
	fsys := fstest.MapFS{
		"getting-started.md": &fstest.MapFile{Data: []byte("# Getting Started\nCreate a lead from CRM > Leads > New.\n")},
	}
	store := &fakeHelpStore{}
	embedder := &ai.FakeEmbedder{Dim: 4}

	res, err := IngestFS(context.Background(), embedder, store, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 1 || res.Ingested[0] != "getting-started" {
		t.Fatalf("want ingested=[getting-started], got %+v", res.Ingested)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("want no failures, got %+v", res.Failed)
	}
	chunks := store.docs["getting-started"]
	if len(chunks) != 1 || chunks[0].Section != "Getting Started" {
		t.Fatalf("store did not receive the expected chunk, got %+v", chunks)
	}
}

func TestIngestFS_OneFileFailingDoesNotStopOthers(t *testing.T) {
	fsys := fstest.MapFS{
		"good.md": &fstest.MapFile{Data: []byte("# Good\nFine content.\n")},
		"bad.md":  &fstest.MapFile{Data: []byte("# Bad\nWill fail to store.\n")},
	}
	store := &fakeHelpStore{failOn: map[string]error{"bad": errors.New("db down")}}
	embedder := &ai.FakeEmbedder{Dim: 4}

	res, err := IngestFS(context.Background(), embedder, store, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 1 || res.Ingested[0] != "good" {
		t.Fatalf("want ingested=[good], got %+v", res.Ingested)
	}
	if msg, ok := res.Failed["bad"]; !ok || msg == "" {
		t.Fatalf("want a failure recorded for bad, got %+v", res.Failed)
	}
}

func TestIngestFS_EmptyFSProducesEmptyResult(t *testing.T) {
	res, err := IngestFS(context.Background(), &ai.FakeEmbedder{Dim: 4}, &fakeHelpStore{}, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 0 || len(res.Failed) != 0 {
		t.Fatalf("want empty result, got %+v", res)
	}
}

func TestIngestFS_EmbedFailureIsRecordedPerDoc(t *testing.T) {
	fsys := fstest.MapFS{
		"doc.md": &fstest.MapFile{Data: []byte("# Doc\nContent.\n")},
	}
	embedder := &ai.FakeEmbedder{Err: errors.New("ollama unreachable")}

	res, err := IngestFS(context.Background(), embedder, &fakeHelpStore{}, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 0 {
		t.Fatalf("want no successes, got %+v", res.Ingested)
	}
	if msg, ok := res.Failed["doc"]; !ok || msg == "" {
		t.Fatalf("want a failure recorded for doc, got %+v", res.Failed)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ai/helpdocs/... -run TestIngestFS -v`
Expected: FAIL with `undefined: IngestFS` (and `undefined: HelpStore`, `undefined: Result`)

- [ ] **Step 3: Write the implementation**

`ai/helpdocs/ingest.go`:

```go
package helpdocs

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"stonesuite-backend/ai"
)

// HelpStore is the write side this package depends on — satisfied by
// *ai.CPHelpStore. Defined here (point of use) so IngestFS is testable
// without a real database.
type HelpStore interface {
	ReplaceDoc(ctx context.Context, docKey string, chunks []ai.HelpChunk) error
}

// Result reports per-doc outcomes from IngestFS. A doc key appears in
// exactly one of Ingested or Failed. JSON tags keep the reindex-help HTTP
// response consistent with this codebase's lowercase-key convention (see
// AskResult, and the "enqueued" key in AIOps.Reindex's response).
type Result struct {
	Ingested []string          `json:"ingested"`
	Failed   map[string]string `json:"failed"` // doc key -> error message
}

// IngestFS chunks every *.md file in fsys by heading, embeds each section
// via embedder, and replaces that doc's chunks in store (keyed by the
// file's base name without extension). A per-file failure is recorded in
// Result.Failed and does not stop the remaining files — the caller (the
// rag-ingest-help CLI or the reindex-help HTTP handler) decides how to
// surface partial failure.
func IngestFS(ctx context.Context, embedder ai.Embedder, store HelpStore, fsys fs.FS) (Result, error) {
	names, err := fs.Glob(fsys, "*.md")
	if err != nil {
		return Result{}, fmt.Errorf("glob: %w", err)
	}
	res := Result{Failed: map[string]string{}}
	for _, name := range names {
		docKey := strings.TrimSuffix(name, filepath.Ext(name))
		if err := ingestOne(ctx, embedder, store, fsys, name, docKey); err != nil {
			res.Failed[docKey] = err.Error()
			continue
		}
		res.Ingested = append(res.Ingested, docKey)
	}
	return res, nil
}

// ingestOne chunks one file, embeds each section, and replaces its doc_key's
// chunks in store.
func ingestOne(ctx context.Context, embedder ai.Embedder, store HelpStore, fsys fs.FS, name, docKey string) error {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	sections := ChunkMarkdown(string(raw), docKey)
	if len(sections) == 0 {
		return fmt.Errorf("no content to chunk")
	}

	chunks := make([]ai.HelpChunk, 0, len(sections))
	for _, sec := range sections {
		vecs, err := embedder.Embed(ctx, []string{sec.Content})
		if err != nil {
			return fmt.Errorf("embed section %q: %w", sec.Title, err)
		}
		chunks = append(chunks, ai.HelpChunk{Section: sec.Title, Content: sec.Content, Embedding: vecs[0]})
	}
	if err := store.ReplaceDoc(ctx, docKey, chunks); err != nil {
		return fmt.Errorf("replace doc: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ai/helpdocs/... -v`
Expected: PASS (all tests in the package, including `TestChunkMarkdown` from Task 1)

- [ ] **Step 5: Full build/test check**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add ai/helpdocs/ingest.go ai/helpdocs/ingest_test.go
git commit -m "feat: add IngestFS orchestration to ai/helpdocs"
```

---

### Task 3: Embed `docs/*.md` at compile time

**Files:**
- Create: `docs/embed.go`
- Create: `docs/embed_test.go`

- [ ] **Step 1: Write the failing test**

`docs/embed_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./docs/... -v`
Expected: FAIL — `package docs: no Go files` (the package doesn't exist yet)

- [ ] **Step 3: Write the implementation**

`docs/embed.go`:

```go
// Package docs embeds this directory's markdown files into the binary at
// compile time, so app-help ingestion (ai/helpdocs, via
// POST /api/platform/ai/reindex-help) doesn't need the source tree present
// at runtime — see docs/superpowers/specs/2026-07-04-rag-ingest-help-design.md.
package docs

import "embed"

//go:embed *.md
var FS embed.FS
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./docs/... -v`
Expected: PASS

- [ ] **Step 5: Full build/test check**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add docs/embed.go docs/embed_test.go
git commit -m "feat: embed docs/*.md into the binary via go:embed"
```

---

### Task 4: Rewrite `cmd/rag-ingest-help` as a thin wrapper over `ai/helpdocs`

**Files:**
- Modify: `cmd/rag-ingest-help/main.go`
- Delete: `cmd/rag-ingest-help/chunk.go`
- Delete: `cmd/rag-ingest-help/chunk_test.go`

- [ ] **Step 1: Replace `cmd/rag-ingest-help/main.go`**

```go
// Command rag-ingest-help ingests markdown docs into cp_rag_chunks (the
// control-plane app-help corpus), chunked by heading and embedded via the
// self-hosted Ollama nomic-embed-text model (ADR-001). Idempotent: each run
// replaces every chunk for a doc_key, so re-running never accumulates stale
// sections.
//
// Usage:
//
//	go run ./cmd/rag-ingest-help [docs-dir]
//
// With no argument, ingests the docs compiled into the binary at build time
// (stonesuite-backend/docs). Pass a directory to preview uncommitted doc
// edits before they're compiled in. Reads CONTROL_PLANE_DB_URL,
// OLLAMA_BASE_URL, and AI_EMBED_MODEL from the environment / .env file (same
// as the server).
//
// In production, prefer POST /api/platform/ai/reindex-help (platform-admin
// only) — it runs the same ai/helpdocs logic in-process on the already-
// deployed backend, with no SSH or local toolchain required.
package main

import (
	"context"
	"io/fs"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/helpdocs"
	"stonesuite-backend/config"
	"stonesuite-backend/docs"
)

func main() {
	config.Load()
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	var docsFS fs.FS = docs.FS
	if len(os.Args) > 1 {
		docsFS = os.DirFS(os.Args[1])
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}
	defer pool.Close()

	embedder := ai.NewOllamaDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel)
	store := ai.NewCPHelpStore(pool)

	res, err := helpdocs.IngestFS(ctx, embedder, store, docsFS)
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}
	for _, key := range res.Ingested {
		log.Printf("OK %s", key)
	}
	for key, msg := range res.Failed {
		log.Printf("FAILED %s: %s", key, msg)
	}
	if len(res.Failed) > 0 {
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Delete the now-duplicated files**

```bash
rm cmd/rag-ingest-help/chunk.go cmd/rag-ingest-help/chunk_test.go
```

- [ ] **Step 3: Full build/test check**

Run: `go build ./... && go test ./...`
Expected: PASS — `cmd/rag-ingest-help` now imports `ai/helpdocs` and `docs` instead of defining its own `ChunkMarkdown`.

- [ ] **Step 4: Manual smoke test against local dev (optional but recommended)**

If you have `CONTROL_PLANE_DB_URL`/`OLLAMA_BASE_URL` set for local dev:

Run: `go run ./cmd/rag-ingest-help`
Expected: `OK ai-assistant` logged, exit code 0.

- [ ] **Step 5: Commit**

```bash
git add cmd/rag-ingest-help/main.go
git rm cmd/rag-ingest-help/chunk.go cmd/rag-ingest-help/chunk_test.go
git commit -m "refactor: rag-ingest-help CLI delegates to ai/helpdocs"
```

---

### Task 5: Add the platform-admin `IsPlatformAdmin` interface and extend `AIOps`

**Files:**
- Modify: `controllers/ai.go`
- Modify: `controllers/ai_test.go`
- Modify: `main.go`

This task changes `NewAIOps`'s signature, which ripples to every call site (production wiring in `main.go`, and the two existing tests). All three must change together to keep the build green.

- [ ] **Step 1: Update `controllers/ai.go` — struct, interface, constructor**

In `controllers/ai.go`, change the import block and the `AIOps`/`NewAIOps` definitions:

```go
import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/helpdocs"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/docs"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)
```

Replace the existing `AIOps` struct and `NewAIOps` function:

```go
// platformAdminChecker is the point-of-use interface ReindexHelp depends on
// for its admin gate — satisfied by *tenancy.ControlPlane. Defined here so
// the gate is testable without a real database.
type platformAdminChecker interface {
	IsPlatformAdmin(ctx context.Context, identityID string) (bool, error)
}

// AIOps serves the tenant AI assistant: POST /api/tenant/ai/ask (RAG chat),
// POST /api/tenant/ai/reindex (admin: re-enqueue every CRM record), and
// POST /api/platform/ai/reindex-help (platform admin: re-embed app-help
// docs). queryEmbed, docEmbed, and llm are injected so tests can substitute
// ai.FakeEmbedder / ai.FakeLLM — no network calls in tests.
type AIOps struct {
	cpPool     *pgxpool.Pool
	queryEmbed ai.Embedder
	docEmbed   ai.Embedder
	llm        ai.LLMClient
	cp         platformAdminChecker
}

// NewAIOps constructs the handler group. queryEmbed MUST apply the
// search_query: prefix (see ai.NewOllamaQueryEmbedder); docEmbed MUST apply
// the search_document: prefix (see ai.NewOllamaDocEmbedder).
func NewAIOps(cpPool *pgxpool.Pool, queryEmbed ai.Embedder, llm ai.LLMClient, cp platformAdminChecker, docEmbed ai.Embedder) *AIOps {
	return &AIOps{cpPool: cpPool, queryEmbed: queryEmbed, llm: llm, cp: cp, docEmbed: docEmbed}
}
```

(`docs` and `helpdocs` imports are unused until Step 2 adds `ReindexHelp` in Task 6 — Go will fail the build on an unused import, so add the handler in the same commit rather than leaving this task half-done. Task 6 below continues directly from this file state without an intermediate commit.)

- [ ] **Step 2: Update the two existing test call sites**

In `controllers/ai_test.go`, change both:

```go
h := NewAIOps(nil, nil, nil)
```

to:

```go
h := NewAIOps(nil, nil, nil, nil, nil)
```

(Both tests only exercise the unauthenticated-rejection path, which returns before touching any of the new fields, so `nil` remains valid for all five arguments.)

- [ ] **Step 3: Update `main.go`'s call site**

Change:

```go
aiOps := controllers.NewAIOps(
	cpPool,
	ai.NewOllamaQueryEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel),
	ai.NewLLM(config.AppConfig.AILLMProvider, llmAPIKey, config.AppConfig.AIChatModel),
)
mux.Handle("POST /api/tenant/ai/ask", tenantChain(aiOps.Ask))
mux.Handle("POST /api/tenant/ai/reindex", tenantChain(aiOps.Reindex))
```

to:

```go
aiOps := controllers.NewAIOps(
	cpPool,
	ai.NewOllamaQueryEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel),
	ai.NewLLM(config.AppConfig.AILLMProvider, llmAPIKey, config.AppConfig.AIChatModel),
	cp,
	ai.NewOllamaDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel),
)
mux.Handle("POST /api/tenant/ai/ask", tenantChain(aiOps.Ask))
mux.Handle("POST /api/tenant/ai/reindex", tenantChain(aiOps.Reindex))
mux.Handle("POST /api/platform/ai/reindex-help", middleware.RequireAuth(http.HandlerFunc(aiOps.ReindexHelp)))
```

(`cp` — the `*tenancy.ControlPlane` — is already in scope at this point in `main.go`; it satisfies `platformAdminChecker` structurally via its existing `IsPlatformAdmin(ctx, identityID) (bool, error)` method, no changes to `tenancy.ControlPlane` needed. `aiOps.ReindexHelp` doesn't exist yet — this compiles once Task 6's handler is added; do not run the build between Task 5 and Task 6.)

Continue directly to Task 6 — do not commit or build yet (the `docs`/`helpdocs` imports and the `ReindexHelp` route reference would fail to compile on their own).

---

### Task 6: Add the `ReindexHelp` handler and its tests

**Files:**
- Modify: `controllers/ai.go`
- Modify: `controllers/ai_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `controllers/ai_test.go` (needs `context`, `stonesuite-backend/middleware` imports added to the file's import block):

```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
)

type fakePlatformAdminChecker struct {
	isAdmin bool
	err     error
}

func (f *fakePlatformAdminChecker) IsPlatformAdmin(_ context.Context, _ string) (bool, error) {
	return f.isAdmin, f.err
}

func TestAIOpsReindexHelp_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/platform/ai/reindex-help", nil)
	w := httptest.NewRecorder()

	h.ReindexHelp(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAIOpsReindexHelp_NonAdminRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, &fakePlatformAdminChecker{isAdmin: false}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/platform/ai/reindex-help", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "u1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ReindexHelp(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
```

(No test exercises the success path against a real `cpPool`/Ollama — matching this codebase's existing convention of testing auth/permission gates at the unit level and verifying the actual ingestion via manual/integration testing, same as `Ask` and `Reindex` above it in this file.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./controllers/... -run TestAIOpsReindexHelp -v`
Expected: FAIL with `h.ReindexHelp undefined (type *AIOps has no field or method ReindexHelp)`

- [ ] **Step 3: Write the handler**

Add to `controllers/ai.go`, after the existing `Reindex` method:

```go
// ReindexHelp handles POST /api/platform/ai/reindex-help. Platform-admin
// only. Re-embeds every docs/*.md file (compiled into the binary via
// stonesuite-backend/docs) into cp_rag_chunks — run after editing any file
// docs/ covers. Unlike Reindex (which enqueues CRM records for a background
// worker), this embeds synchronously in the request: the app-help corpus is
// small enough (today: one file) that a background queue would be pure
// overhead.
func (h *AIOps) ReindexHelp(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	isAdmin, err := h.cp.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !isAdmin {
		logSecurityEvent(r, "ai_reindex_help_denied")
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}

	store := ai.NewCPHelpStore(h.cpPool)
	res, err := helpdocs.IngestFS(r.Context(), h.docEmbed, store, docs.FS)
	if err != nil {
		slog.Error("reindex help failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to reindex app-help docs.")
		return
	}

	logSecurityEvent(r, "ai_reindex_help", "ingested", len(res.Ingested), "failed", len(res.Failed))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": res})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./controllers/... -v`
Expected: PASS (all tests in the package, including the pre-existing `TestAIOpsAsk_UnauthenticatedRejected` / `TestAIOpsReindex_UnauthenticatedRejected` updated in Task 5)

- [ ] **Step 5: Full build/test check**

Run: `go build ./... && go test ./...`
Expected: PASS — this closes out the signature change started in Task 5 (the `docs`/`helpdocs` imports are now used, `aiOps.ReindexHelp` now exists).

- [ ] **Step 6: Commit**

```bash
git add controllers/ai.go controllers/ai_test.go main.go
git commit -m "feat: add POST /api/platform/ai/reindex-help endpoint"
```

---

### Task 7: Manual verification against production

**Files:** none (deployment + manual smoke test only)

- [ ] **Step 1: Deploy**

```bash
fly deploy -a stonesuite-backend
```

Expected: clean rolling deploy, health check passes (`GET /api` → 200), matching every prior deploy this session.

- [ ] **Step 2: Call the new endpoint with a platform-admin JWT**

```bash
curl -s -X POST https://stonesuite-backend.fly.dev/api/platform/ai/reindex-help \
  -H "Authorization: Bearer <platform admin JWT>"
```

Expected: `{"success":true,"data":{"ingested":["ai-assistant"],"failed":{}}}`

- [ ] **Step 3: Confirm no more SSH/toolchain workaround is needed**

This replaces the process from earlier in this session (SSH console + base64 tarball + `apk add go` + `go mod download`) entirely — the endpoint call in Step 2 is now the complete procedure for re-ingesting app-help docs.

- [ ] **Step 4: Ask the AI chat a how-to question to confirm app-help retrieval now works**

In the browser (dev.stonesuite.app), ask the AI assistant: "How do I create a new lead in StoneSuite?" — this previously answered "I don't have that information" because `cp_rag_chunks` was empty. It should now cite the newly-ingested `ai-assistant` doc if its content is relevant to the question (note: `docs/ai-assistant.md` is about the AI assistant feature itself, not general how-to content, so the honest answer may still be "I don't have that information" for this specific question — that's correct behavior, not a bug; the endpoint's job is just to make ingestion possible, not to guarantee every question has an answer in a one-file corpus).

---

## Self-review notes (already applied above)

- **Spec coverage:** every section of the design spec maps to a task — embed (Task 3), package move (Tasks 1–2), CLI wrapper (Task 4), handler + auth (Tasks 5–6), manual verification (Task 7). CI auto-triggering was explicitly out of scope and has no task, matching the spec's Non-goals.
- **Placeholder scan:** no TBD/TODO; every code step has complete code.
- **Type consistency:** `platformAdminChecker.IsPlatformAdmin(ctx, identityID string) (bool, error)` matches `tenancy.ControlPlane.IsPlatformAdmin`'s real signature (verified against `tenancy/registry.go`) in every task that references it. `helpdocs.HelpStore.ReplaceDoc(ctx, docKey string, chunks []ai.HelpChunk) error` matches `ai.CPHelpStore.ReplaceDoc`'s real signature (verified against `ai/cphelp.go`) everywhere it's used.

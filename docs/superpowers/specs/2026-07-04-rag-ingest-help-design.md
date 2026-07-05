# Design: rag-ingest-help as an in-process admin endpoint

**Status:** Approved
**Date:** 2026-07-04

## Problem

`cmd/rag-ingest-help` (chunks `docs/*.md`, embeds each section via the
self-hosted Ollama embedder, and upserts into the control-plane's
`cp_rag_chunks` table) is a separate Go binary that isn't built into the
deployed image — the Dockerfile only compiles the root `main.go` into
`/app/server`; `docs/` isn't copied into the final Alpine stage either.

Running it against production today requires: SSH into a `stonesuite-backend`
machine, base64-smuggle a tarball of the needed source through the SSH exec
command (since the box has no git access to a private repo and no simple file
upload), `apk add go` to get a Go toolchain onto a box that's never had one,
`go mod download` (which itself downloads a newer toolchain per `go.mod`'s
`go 1.25.11` directive), then `go run ./cmd/rag-ingest-help`. This is slow
(multiple minutes), fragile (a concurrent `fly deploy` replaces the machine
mid-SSH-session and silently kills the run), and not something anyone would
want to repeat.

Per `docs/ai-assistant.md`, this needs to run "after editing any file this doc
set covers" — i.e. it's a recurring operational need, not a one-off backfill,
so the friction above will resurface every time app-help docs change.

## Goal

Make re-ingesting app-help docs a single authenticated HTTP call against the
already-running, already-deployed backend — no SSH, no toolchain install, no
race with deploys, no new deploy artifact.

## Non-goals

- **CI auto-triggering** (e.g. `deploy-backend.yml` calling this endpoint
  automatically after a deploy that touches `docs/`) is explicitly out of
  scope for this iteration. The manual endpoint alone removes the actual
  pain; auto-triggering is a small, separate follow-up if manual triggering
  ever becomes annoying in practice.
- No change to the tenant-side CRM record RAG pipeline (`ai/index`,
  `crmstore/rag_loader.go`, the `/api/tenant/ai/*` routes) — this is scoped
  entirely to the control-plane app-help corpus.

## Architecture

### 1. Embed docs at compile time

New file `docs/embed.go`:

```go
package docs

import "embed"

//go:embed *.md
var FS embed.FS
```

This bakes every `docs/*.md` file into any binary that imports
`stonesuite-backend/docs` — including `./server` — with zero Dockerfile
changes (Go embeds are resolved at compile time, before the Docker build's
final stage even exists).

### 2. Move ingestion logic into an importable package

`cmd/rag-ingest-help/chunk.go` (`ChunkMarkdown`, `Section`) and the
ingest-one-file orchestration in `cmd/rag-ingest-help/main.go` currently live
in `package main`, which can't be imported by `controllers/`. They move to a
new package `ai/helpdocs` (mirroring the existing `ai/index` subpackage
pattern for the tenant-side indexing worker):

- `ai/helpdocs/chunk.go` — `ChunkMarkdown`, `Section` (moved as-is, plus
  `chunk_test.go`).
- `ai/helpdocs/ingest.go` — new orchestration:

  ```go
  package helpdocs

  // HelpStore is the write side this package depends on (point-of-use
  // interface over ai.CPHelpStore, so ingestion is testable without a DB).
  type HelpStore interface {
      ReplaceDoc(ctx context.Context, docKey string, chunks []ai.HelpChunk) error
  }

  // Result reports per-file outcomes so callers (HTTP handler or CLI) can
  // decide how to surface partial failure.
  type Result struct {
      Ingested []string // doc keys successfully replaced
      Failed   map[string]string // doc key -> error message
  }

  // IngestFS chunks every *.md file in fsys, embeds each section via
  // embedder, and replaces that doc's chunks in store. A per-file failure is
  // recorded in Result.Failed and does not stop the remaining files —
  // matches the existing CLI's "continue on failure, report the total"
  // behavior.
  func IngestFS(ctx context.Context, embedder ai.Embedder, store HelpStore, fsys fs.FS) (Result, error)
  ```

  `IngestFS` takes an `fs.FS` (not a directory path) so both the embedded
  `docs.FS` and an on-disk `os.DirFS(dir)` satisfy it identically.

### 3. New platform-admin endpoint

`AIOps` (`controllers/ai.go`) gains two fields, wired from `main.go`:

- `cp *tenancy.ControlPlane` — for the platform-admin check (same
  `IsPlatformAdmin` call `TenantOps.requirePlatformAdmin` already uses;
  `AIOps` gets its own equivalent small helper rather than depending on
  `TenantOps`).
- `docEmbed ai.Embedder` — a doc-prefixed embedder
  (`ai.NewOllamaDocEmbedder(...)`, matching the instance
  `startRAGIndexing` already constructs for the tenant-side worker).

New handler:

```go
// ReindexHelp handles POST /api/platform/ai/reindex-help. Platform-admin
// only. Re-embeds every docs/*.md file into cp_rag_chunks — run after
// editing any file docs/ covers.
func (h *AIOps) ReindexHelp(w http.ResponseWriter, r *http.Request)
```

- 403 if the caller isn't a platform admin (`fail(w, http.StatusForbidden, ...)`,
  same as every other platform-admin route).
- Calls `helpdocs.IngestFS(ctx, h.docEmbed, ai.NewCPHelpStore(h.cpPool), docs.FS)`.
- Response: `{"success": true, "data": {"ingested": [...], "failed": {...}}}`
  on a request that reached the handler; a per-file embed/DB failure is
  reported in `data.failed`, not a 5xx — the endpoint did its job
  (attempted every file), individual file failures are data, not a
  request-level error. A total inability to run (e.g. Ollama unreachable for
  every file) still surfaces clearly via a fully-populated `failed` map, and
  the caller can retry (idempotent — `ReplaceDoc` only ever affects the doc
  keys it processes).
- Route registration in `main.go`, alongside the existing `/api/tenant/ai/*`
  block: `mux.Handle("POST /api/platform/ai/reindex-help", middleware.RequireAuth(http.HandlerFunc(aiOps.ReindexHelp)))`.

### 4. `cmd/rag-ingest-help` becomes a thin wrapper

Still useful for local dev — in particular, previewing uncommitted doc edits
before they're compiled into a build. Its `main.go` shrinks to: resolve an
`fs.FS` (the `docs-dir` CLI arg via `os.DirFS` if given, else the embedded
`docs.FS`), construct the same `ai.NewOllamaDocEmbedder` +
`ai.NewCPHelpStore`, call `helpdocs.IngestFS`, print the `Result`, exit
non-zero if `Result.Failed` is non-empty. No behavior change to its existing
usage (`go run ./cmd/rag-ingest-help [docs-dir]`, reads
`CONTROL_PLANE_DB_URL`/`OLLAMA_BASE_URL`/`AI_EMBED_MODEL` from the
environment).

## Data flow

1. Compile time: `docs/*.md` &rarr; embedded into `docs.FS` &rarr; linked into `./server`.
2. You (or a future CI step) call
   `POST /api/platform/ai/reindex-help` with a platform-admin JWT.
3. Handler checks `IsPlatformAdmin` &rarr; 403 if not.
4. `helpdocs.IngestFS` walks `docs.FS`, chunks each file by heading
   (`ChunkMarkdown`), embeds each section via the self-hosted Ollama embedder
   (already reachable over `.internal` from the running process — no new
   networking), and calls `CPHelpStore.ReplaceDoc` per doc key inside one
   transaction (delete + reinsert), same as today.
5. Handler returns per-doc-key success/failure.

## Error handling

- Platform-admin check failure &rarr; 403, `logSecurityEvent` (matches every
  other admin-gated route in this codebase).
- Embed or DB failure for one file &rarr; recorded in the response's `failed`
  map with the underlying error message; does not abort remaining files
  (matches the current CLI's per-file continue-on-failure behavior).
- No partial writes: `ReplaceDoc`'s delete+insert stays in one transaction
  per doc key, unchanged from today.

## Testing plan

- `ai/helpdocs/chunk_test.go` — moved as-is (already-passing tests for
  `ChunkMarkdown`).
- `ai/helpdocs/ingest_test.go` — new table-driven tests for `IngestFS` using
  `ai.FakeEmbedder` and an in-memory fake `HelpStore` (map-backed), covering:
  all-files-succeed, one-file-fails-others-continue, empty `fs.FS` (no `.md`
  files &rarr; empty `Result`, not an error).
- `controllers/ai_test.go` — new test for `AIOps.ReindexHelp`: non-admin
  caller &rarr; 403; platform admin &rarr; 200 with the expected `Result` shape,
  using fakes (no real DB/Ollama, matching every other handler test in this
  file).
- `cmd/rag-ingest-help/chunk_test.go` deleted (moved).
- `go build ./... && go test ./...` must stay green throughout.

## Migration / rollout

1. Add `docs/embed.go`.
2. Create `ai/helpdocs` package (move + adapt `chunk.go`/`chunk_test.go`,
   add `ingest.go`/`ingest_test.go`).
3. Update `cmd/rag-ingest-help/main.go` to the thin wrapper.
4. Add `AIOps.ReindexHelp` + wire `cp`/`docEmbed` fields + route in
   `main.go`.
5. `go build ./... && go test ./...`.
6. Deploy.
7. Use the new endpoint to run the still-outstanding app-help ingestion this
   session was blocked on (only `docs/ai-assistant.md` exists today, so this
   is a small first run).

## Open questions resolved during brainstorming

- **Recurring vs. one-off?** Recurring — matches existing docs guidance, so
  this is worth building properly rather than living with the SSH workaround.
- **Trigger mechanism?** HTTP endpoint (Option A) over a CLI flag on the
  deployed binary (Option B) or a Fly `release_command` (Option C — rejected:
  runs before the app boots, so the backend-owned Ollama lifecycle isn't up
  yet at that point, and it would re-embed unchanged docs on every unrelated
  deploy). The HTTP endpoint fits the existing `/api/platform/*` convention,
  can't be killed by a concurrent deploy, and leaves a clean path to CI
  automation later without further design work.
- **CI automation now?** No — manual trigger only, per Non-goals above.

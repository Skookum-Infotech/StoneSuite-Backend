# AI Assistant (RAG)

StoneSuite's AI assistant answers questions about a tenant's CRM records (leads,
prospects, customers) and about how to use the app, using retrieval-augmented
generation. Every answer is grounded only in retrieved context and scoped to what
the asking user is allowed to see.

## How it works

1. A record write (create/update/transition/delete/convert on a lead, prospect, or
   customer) enqueues a job on that tenant's `rag_index_queue`.
2. A per-tenant background worker drains the queue every few seconds: it loads the
   record, renders it to text, embeds the text, and upserts the vector into that
   tenant's `rag_chunks` table.
3. A question to `POST /api/tenant/ai/ask` is embedded, then matched against
   `rag_chunks` (RBAC-scoped to the caller's `all`/`team`/`own` permission) and
   against `cp_rag_chunks` (unscoped app-help docs, shared across all tenants).
   The LLM is instructed to answer only from that retrieved context.

## Security model

- **Tenant isolation**: `rag_chunks` lives in each tenant's own database. Retrieval
  can never see another tenant's vectors — there is no shared record vector store.
- **RBAC scope**: the caller's most restrictive granted scope across the CRM
  resources (lead/prospect/customer) is ANDed onto the similarity search. An
  unrecognized or ungranted scope returns zero results (fail closed), never a
  wider result set.
- **App-help** (`cp_rag_chunks`) has no scope clause — it's identical, non-private
  content for every tenant.

## Environment variables

| Variable | Required | Default | Notes |
|----------|----------|---------|-------|
| `AI_EMBED_PROVIDER` | No | `ollama` | Pinned per ADR-001 — do not change without a re-index plan. |
| `AI_EMBED_MODEL` | No | `snowflake-arctic-embed:m` | Must stay in sync with `AI_EMBED_DIM`. Changing this requires re-embedding every existing chunk — different models are different vector spaces even at the same dimension. |
| `AI_EMBED_DIM` | No | `768` | Must match the `vector(N)` columns in schema.sql. |
| `OLLAMA_BASE_URL` | Yes (for embeddings to work) | `http://localhost:11434` | Points at the self-hosted embedder box — `http://stonesuite-ollama.internal:11434` in prod (see below). |
| `AI_LLM_PROVIDER` | No | `gemini` | `gemini` or `groq`. |
| `GEMINI_API_KEY` | If using Gemini | — | Free tier. |
| `GROQ_API_KEY` | If using Groq | — | Free tier. |
| `AI_CHAT_MODEL` | No | `gemini-flash-latest` | Google-maintained alias for their current recommended flash model — prefer this over pinning a dated version (e.g. `gemini-1.5-flash`), which breaks once Google retires it. Set to a Groq model name (e.g. `llama-3.1-8b-instant`) when `AI_LLM_PROVIDER=groq`. |
| `FLY_OLLAMA_API_TOKEN` | Prod only | — | Deploy-scoped token for the Ollama app (see lifecycle section below). Unset = lifecycle control skipped entirely. |
| `FLY_OLLAMA_APP_NAME` | No | `stonesuite-ollama` | Which Fly app the backend starts/stops. |

## The Ollama embedder box

Embeddings are self-hosted (data residency from day one, per ADR-001) — the model
never leaves your infrastructure. Nothing is sent to a third-party embeddings API.

In production this runs as a second Fly app, `stonesuite-ollama` (see `ollama/`:
`Dockerfile` + `fly.toml`), reachable only over Fly's private network at
`http://stonesuite-ollama.internal:11434` (direct 6PN, not flycast — see below)
— **it has no public IP and is never internet-reachable.**

### Networking: `.internal`, not flycast

Fly's private-proxy address (`stonesuite-ollama.flycast`) was tried first, since
it's the mechanism that supports autostart on private traffic. Verified broken
live: Ollama's own logs never saw the backend's requests at all — every call
reset before reaching the app, even with the Machine confirmed healthy and
running. Direct 6PN addressing (`.internal`) bypasses Fly's proxy layer
entirely and was confirmed working (curl from a backend Machine returns
correctly). The trade-off: `.internal` has no autostart of its own — which is
fine, since lifecycle is handled explicitly (next section).

### Lifecycle: owned by the backend, not Fly's scaling

The box is started/stopped explicitly by `stonesuite-backend` itself
(`services.OllamaLifecycle`, wired in `main.go`) via Fly's Machines API — on
the backend's own boot (fired in the background so a ~10s cold model load
doesn't block startup) and on its own graceful shutdown (`SIGTERM` handler).
`ollama/fly.toml` sets `min_machines_running = 0` and disables Fly's own
`auto_start`/`auto_stop_machines` so nothing fights the explicit control.

This ties Ollama's uptime to the backend's process lifetime — which, since the
backend itself runs scale-to-zero, means Ollama is only ever running while the
backend is. **Known limitation:** `stonesuite-backend` can scale to more than
one Machine under load; each independently calls start/stop with no
coordination, so one Machine going idle and calling stop while a sibling
Machine is still actively serving traffic would kill Ollama out from under it.
Acceptable at current traffic (concurrent multi-Machine periods are rare), but
worth revisiting (e.g. a shared "how many backend instances are up" counter)
if multi-Machine concurrency becomes routine.

The pulled model persists across restarts on a mounted volume (`ollama_data`).

For local dev, provision a small CPU-only box (or run Ollama directly on your
machine):

```bash
ollama pull snowflake-arctic-embed:m
```

Record indexing can tolerate a cold start (it's async, off the request path —
see the worker below), but note `POST /api/tenant/ai/ask` embeds the caller's
question **synchronously**, so a cold Ollama machine adds latency to that one
request. `snowflake-arctic-embed:m` (~109M params) was chosen for exactly this
reason: strong retrieval quality (MTEB-tuned) at a size that stays fast even on
CPU-only shared infra, at the same 768 dimensions as before — a straight
model swap, no schema migration required.

## Re-ingesting app-help docs

Run after editing any file this doc set covers:

```bash
go run ./cmd/rag-ingest-help
```

Reads every `docs/*.md`, chunks by heading, embeds each section, and replaces
that file's chunks in `cp_rag_chunks`. Idempotent — safe to re-run.

## Forcing a full reindex

After changing what a record renders as, an admin can force every CRM record to
re-embed:

```bash
curl -X POST https://<host>/api/tenant/ai/reindex \
  -H "Authorization: Bearer <admin JWT>"
```

Requires `workflow_config:configure` (the same permission that gates other
workspace-admin actions). Enqueues every lead/prospect/customer record; the
background worker drains it at its normal pace.

**After an `AI_EMBED_MODEL` change**, run this for every tenant *and* re-run
`go run ./cmd/rag-ingest-help` (above) for `cp_rag_chunks` — the reindex endpoint
only covers tenant CRM records, not app-help docs. Different embedding models
produce incompatible vector spaces even at the same dimension, so every stored
chunk (both tables) must be re-embedded with the new model; `Upsert` overwrites
in place keyed by `source_id`, so no manual `TRUNCATE` is needed as long as every
chunk gets revisited.

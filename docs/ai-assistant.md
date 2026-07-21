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
   `rag_chunks` (RBAC-scoped to the caller's `all`/`own` permission) and
   against `cp_rag_chunks` (unscoped app-help docs, shared across all tenants).
   The LLM is instructed to answer only from that retrieved context. Questions
   over 2000 characters are rejected with `400` before embedding — well under
   the embedder's ~512-token context window, so a long question fails fast
   with a clear message instead of a generic error from Ollama.

## Retrieval: hybrid + a relevance floor

Each of the two sources above (tenant records, app-help) is actually searched
twice and the results fused:

- **Vector arm** — pgvector cosine similarity (`embedding <=> $1`, HNSW index).
  Finds conceptually related content even with no shared words.
- **Lexical arm** — Postgres full-text search (`content_tsv`, a generated
  `tsvector` + GIN index, `simple` config so identifiers like
  `INC-2023-Q4-011` survive tokenization unstemmed). Finds exact terms and
  rare tokens a 768-dim embedding can blur — record numbers, names, codes.

The two ranked lists are merged with **Reciprocal Rank Fusion** (`ai/fuse.go`,
`rrfK = 60`): a chunk ranked highly in *both* arms outranks one ranked highly
in only one. The lexical arm degrades gracefully — a full-text query error is
logged and retrieval falls back to vector-only rather than failing the whole
`ask` — while the vector arm staying broken is still fatal, since it's the
primary path.

`buildScopedSearch` and `buildScopedLexicalSearch` (`ai/store.go`) share one
`scopeClause` helper, so the RBAC scope is guaranteed identical on both arms —
see `ai/store_scope_test.go` and the `dbtest`-tagged ownership tests in
`ai/store_test.go`.

Because top-k similarity search alone always returns k chunks regardless of
true relevance, a **relevance floor** (`ai/orchestrator.go`,
`relevanceFloorDistance`) drops an arm's results entirely if none of them
clear the bar — any lexical hit (a literal term match needs no distance
floor) or any vector hit at or under the floor — rather than grounding a
small, weak model in noise it might hallucinate over. Applied per-source, so
a genuinely relevant help match still grounds the answer even when nothing
relevant was found in the tenant's own records, and vice versa.

## Analytical questions: a separate, exact fast-path for pure counts

RAG (retrieval over individual record chunks) fundamentally cannot answer
"how many customers do we have" — there's no single chunk containing a count.
Rather than let those questions reach the LLM and get an honest but
unsatisfying refusal, `POST /api/tenant/ai/ask` classifies the question first
(`controllers/ai_analytical.go`, `classifyCountQuestion`): a narrow
regex/keyword match for "how many" / "count of" / "number of" / "total"
combined with a CRM type word (lead/prospect/customer, or "record(s)"/"crm"
for all three). A match short-circuits straight to `crmstore.Store`'s new
`CountRecords` method — the same RBAC scope (`all`/`own`) and caller
identity the RAG path would have used, just a `SELECT COUNT(*)` instead of
retrieval — and returns a deterministic sentence with **zero LLM calls**: no
generation latency, no hallucination risk, no cost.

**Deliberately narrow scope**: this path answers *unfiltered totals only*. A
question with any date/status/outcome language ("last week", "won", "closed",
"since", etc. — see `filterHintWords`) is rejected by the classifier and falls
through to the normal RAG path, which honestly says "I don't have that
information" — rather than this path silently returning an unfiltered total
mislabeled as the answer to a filtered question. Answering "which customers
won in the last week" correctly would need a reliable status-change timestamp
the schema doesn't have today (the customer table's `customer_approved_at` is
Closed-Won-approval-only, not a general "when did this change status" column)
— a real schema/product decision, not something to guess at silently.

**Golden-question regression set**: `controllers/ai_golden_test.go` runs ~30
realistic CRM questions through this same classifier and asserts the routing
decision (analytical count vs. fall-through to RAG) on every `go test ./...`
— a wrong routing decision here is either a silently-wrong "confidently
exact" number or an unnecessary refusal, so it's covered as a CI regression
gate. It deliberately only covers the classifier, which is pure and needs no
DB/Ollama; grading live RAG answer quality (retrieval recall, citation
correctness, refusal rate) needs a seeded tenant and a running model, which
CI doesn't have today — `ai_refusals_total` / `ai_asks_total` (below) is the
production substitute for that signal.

## Observability

`GET /api/metrics` (Prometheus) exposes AI-specific series alongside the
generic HTTP ones, all defined in `metrics/ai.go`:

- `ai_embed_duration_seconds`, `ai_llm_duration_seconds` — per-stage latency
  histograms for one `/ai/ask` (embed step, chat-completion step).
- `ai_llm_timeouts_total` — chat completions that failed specifically because
  the LLM client's own deadline elapsed (`ai/ollama_llm.go`'s 60s timeout),
  distinct from any other failure.
- `ai_asks_total` / `ai_refusals_total` — **refusal rate** (`ai_refusals_total
  / ai_asks_total`) is the primary answer-quality signal: the share of asks
  where the model said "I don't have that information" instead of answering.
  Watch this around any deploy touching retrieval, the chat model, or
  grounding content. Counts only asks that reach a chat completion — the
  analytical count fast-path (below) is exact and never refuses, so it's
  intentionally excluded.
- `rag_index_queue_pending` / `rag_index_queue_oldest_pending_age_seconds`
  (labeled by tenant) — published every drain tick (`ai/index.Queue.Stats`,
  `main.go`'s `runTenantIndexWorker`); a growing backlog or aging oldest-job
  means indexing is falling behind that tenant's write volume.

`ai.Orchestrator.WithMetrics` wires these in (default: a no-op sink, so
building an `Orchestrator` without it — as every existing test does — records
nothing and behaves exactly as before this existed).

## Security model

- **Tenant isolation**: `rag_chunks` lives in each tenant's own database. Retrieval
  can never see another tenant's vectors — there is no shared record vector store.
- **RBAC scope**: the caller's most restrictive granted scope across the CRM
  resources (lead/prospect/customer) is ANDed onto both the vector and lexical
  search (same `scopeClause`, see above). An unrecognized or ungranted scope
  returns zero results (fail closed), never a wider result set.
- **App-help** (`cp_rag_chunks`) has no scope clause — it's identical, non-private
  content for every tenant.

## Environment variables

| Variable | Required | Default | Notes |
|----------|----------|---------|-------|
| `AI_EMBED_PROVIDER` | No | `ollama` | Pinned per ADR-001 — do not change without a re-index plan. |
| `AI_EMBED_MODEL` | No | `snowflake-arctic-embed:m` | Must stay in sync with `AI_EMBED_DIM`. Changing this requires re-embedding every existing chunk — different models are different vector spaces even at the same dimension. |
| `AI_EMBED_DIM` | No | `768` | Must match the `vector(N)` columns in schema.sql. |
| `OLLAMA_BASE_URL` | Yes | `http://localhost:11434` | Points at the self-hosted box serving both embeddings and chat — `http://stonesuite-ollama.internal:11434` in prod (see below). |
| `AI_CHAT_MODEL` | No | `llama3.2:3b` | An Ollama model tag — must already be pulled on the box (see `ollama/entrypoint.sh`). |
| `FLY_OLLAMA_API_TOKEN` | Prod only | — | Deploy-scoped token for the Ollama app (see lifecycle section below). Unset = lifecycle control skipped entirely. |
| `FLY_OLLAMA_APP_NAME` | No | `stonesuite-ollama` | Which Fly app the backend starts/stops. |

## The Ollama box (embeddings + chat)

Both embeddings and chat completions are fully self-hosted (data residency
from day one, per ADR-001; no third-party LLM account, API key, or quota at
all) — nothing is sent to a third-party AI API. `ai.OllamaLLMClient`
(`ai/ollama_llm.go`) talks to Ollama's `/api/chat` endpoint; `ai.OllamaEmbedder`
talks to `/api/embeddings`. Both hit the same box.

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
backend is.

#### Known limitation: multi-Machine coordination

`stonesuite-backend` can scale to more than
one Machine under load; each independently calls start/stop with no
coordination, so one Machine going idle and calling stop while a sibling
Machine is still actively serving traffic would kill Ollama out from under it.
Acceptable at current traffic (concurrent multi-Machine periods are rare), but
worth revisiting (e.g. a shared "how many backend instances are up" counter)
if multi-Machine concurrency becomes routine.

The pulled models persist across restarts on a mounted volume (`ollama_data`).

For local dev, provision a small box (or run Ollama directly on your machine):

```bash
ollama pull snowflake-arctic-embed:m
ollama pull llama3.2:3b
```

Record indexing can tolerate a cold start (it's async, off the request path —
see the worker below), but note `POST /api/tenant/ai/ask` embeds the caller's
question **and** generates the chat completion **synchronously**, so a cold or
overloaded Ollama machine adds latency to that one request.

### Chat model sizing

`llama3.2:3b` (bumped from `:1b` after live use showed the 1b model
over-refusing — see "Retrieval: hybrid + a relevance floor" above) was chosen
to fit this box's RAM alongside the embedder — but it's genuinely CPU-bound
work, not just a memory concern. A synchronous chat
request has a hard ceiling: `main.go`'s `http.Server.WriteTimeout` forcibly
closes the connection once it elapses, even if the handler is about to
finish with a correct answer — this was long misattributed to "Fly's proxy
dropping slow requests" (see git history), but reproduced live with
`WriteTimeout` at 15s: the backend logged a real 200 while the client saw a
bare connection-closed error. Now set to 90s (comfortably above
`OllamaLLMClient`'s own 60s timeout, so that one fires first and the caller
gets a clean JSON error instead of a dropped connection). Two levers keep a
request comfortably under that ceiling in practice:

- **Context size**: `groundingContent` (`ai/store.go`) caps how much of each
  retrieved chunk reaches the model. Keep this conservative for a CPU-bound
  local model — a long prompt means long prefill time before the model even
  starts generating.
- **`num_predict`** (`ai/ollama_llm.go`): caps how many tokens the model is
  allowed to generate, bounding worst-case generation time regardless of the
  question.

If responses are still too slow after tuning both, the fix is more CPU on
`ollama/fly.toml`'s `[[vm]]` (a modest, fixed Fly infra cost — not a
per-request API bill), not a smaller model or more retries. Even at `:3b`,
this is still noticeably weaker than a larger hosted model — expect it to
occasionally cite less reliably or need a nudge.
`ollama/fly.toml` already runs a dedicated (`performance`) CPU core rather
than a shared one for exactly this reason — CPU-bound inference suffers
badly from shared-vCPU noisy-neighbor throttling.

`OLLAMA_MAX_LOADED_MODELS=2` (`ollama/fly.toml`) keeps both the embed and chat
models resident at once, since every `/ai/ask` call uses both back to back —
without it, Ollama's default eviction would swap one model out to load the
other on nearly every request.

## Known limitations that need frontend changes

Two cold-start/reliability items from the architecture review are blocked on
the separate [StoneSuite frontend repo](https://github.com/Skookum-Infotech/StoneSuite)
and cannot be finished from this backend alone:

- **Streaming responses.** `POST /api/tenant/ai/ask` answers in one shot —
  the client waits out the full embed + retrieval + chat-completion latency
  (worst case, tens of seconds on a cold/CPU-bound Ollama box) before seeing
  anything. Switching Ollama's `/api/chat` call to `stream:true` and relaying
  tokens over SSE/chunked HTTP as they're generated would get a first token
  back in ~1-2s and keep the connection visibly alive, which is a much more
  robust fix for the "Network Error" cold-start UX than raising timeouts
  further. This needs a backend streaming endpoint *and* a frontend consumer
  for it — half the work (frontend) is out of this repo's scope.
- **Frontend request timeout.** The backend's `http.Server.WriteTimeout` is
  90s (see "Chat model sizing" above) specifically so a slow-but-legitimate
  chat completion finishes with a clean JSON response instead of a dropped
  connection. If the frontend's own HTTP client (fetch/axios) times out the
  `/ai/ask` request before 90s, the user sees an error for a request the
  backend actually completed successfully — the frontend's timeout for this
  one endpoint needs to be ≥ 90s. This is a frontend-repo config change; it
  cannot be fixed here.

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

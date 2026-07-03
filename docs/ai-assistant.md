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
| `AI_EMBED_MODEL` | No | `nomic-embed-text` | Must stay in sync with `AI_EMBED_DIM`. |
| `AI_EMBED_DIM` | No | `768` | Must match the `vector(N)` columns in schema.sql. |
| `OLLAMA_BASE_URL` | Yes (for embeddings to work) | `http://localhost:11434` | Points at the self-hosted embedder box. |
| `AI_LLM_PROVIDER` | No | `gemini` | `gemini` or `groq`. |
| `GEMINI_API_KEY` | If using Gemini | — | Free tier. |
| `GROQ_API_KEY` | If using Groq | — | Free tier. |
| `AI_CHAT_MODEL` | No | `gemini-1.5-flash` | Set to a Groq model name (e.g. `llama-3.1-8b-instant`) when `AI_LLM_PROVIDER=groq`. |

## The Ollama embedder box

Embeddings are self-hosted (data residency from day one, per ADR-001) — the model
never leaves your infrastructure. Provision a small CPU-only box:

```bash
ollama pull nomic-embed-text
```

It can scale to zero; a few seconds' cold start on the first embed after idle is
fine, since embedding always happens off the request path (the async index worker
or the CLI below), never inline with a user's write.

## Re-ingesting app-help docs

Run after editing any file this doc set covers:

```bash
go run ./cmd/rag-ingest-help
```

Reads every `docs/*.md`, chunks by heading, embeds each section, and replaces
that file's chunks in `cp_rag_chunks`. Idempotent — safe to re-run.

## Forcing a full reindex

After changing what a record renders as (not after an embedding-model change,
since that's pinned), an admin can force every CRM record to re-embed:

```bash
curl -X POST https://<host>/api/tenant/ai/reindex \
  -H "Authorization: Bearer <admin JWT>"
```

Requires `workflow_config:configure` (the same permission that gates other
workspace-admin actions). Enqueues every lead/prospect/customer record; the
background worker drains it at its normal pace.

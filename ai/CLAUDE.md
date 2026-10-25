# AI / RAG — Claude rules

_Loaded automatically when Claude reads files under `ai/`. Moved verbatim from the root CLAUDE.md._

## AI / RAG (`ai/`)
1. **`ai/` is provider-agnostic and dependency-free of app packages** (Embedder + LLMClient interfaces, RAG orchestrator, record rendering) — same discipline as `query/`. Keep app types out of it.
2. **LLM + embeddings are self-hosted via Ollama** (`ollama/` is its own Fly app), not a hosted API — chosen for data-residency/security. Don't swap in a hosted embeddings/LLM API without that trade-off being decided explicitly.
3. **Help corpus is embedded** (`docs/embed.go`, an explicit end-user-only manifest) and ingested with `go run ./cmd/rag-ingest-help`; reindex via `POST /api/platform/ai/reindex-help`.
4. **Tenant RAG Q&A is `POST /api/tenant/ai/ask`** (auth + TenantResolver); `/api/embeddings` + `/api/chat` proxy the model.
5. **Every business module is answerable through `globalsearch/`'s AI hooks.** A module is *AI-dead* until it has an `addAI(...)` entry (`globalsearch/ai_<domain>.go`: `Load` summarizer, `ListLive`, `Count`, `OwnScoped`, `Label`/`Nouns`) — the `new-module` skill covers it. Grants (`controllers/ai_scope.go`), retrieval scope, counts and record-number lookups are all generated from that registry; the queue carries `rag_index_queue.record_type`; writes re-index through the `workflow.LogAuditFull` observer (a module whose controller never audits is only caught by the 10-minute reconcile). A document's `Core` keys follow the shared convention (`number`, `status`, `customer`/`vendor`, `date`, `due_date`, `grand_total`, `balance`, `memo`, `po_number`, `line_items`) because the fast path reads them by name. Never widen a grant: a module's type is in `Grants` only when `authz.Check(read)` allowed it, and modules without an owner column (`OwnScoped=false`) are treated as `all`.
6. **Help corpus is end-user docs only** (`docs/embed.go` explicit list). A user-visible feature change must update the matching help doc; the corpus re-ingests itself on the next boot when doc content changes (`cp_help_corpus_state`). `TestHelpCorpusIsUserFacing` guards against engineering content.

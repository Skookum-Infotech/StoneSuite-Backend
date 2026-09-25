package controllers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai/index"
	"stonesuite-backend/aisettings"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

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
	cpPool           *pgxpool.Pool
	queryEmbed       ragcore.Embedder
	docEmbed         ragcore.Embedder
	llm              ragcore.LLMClient
	cp               platformAdminChecker
	reranker         ragcore.Reranker
	rerankCandidates int
	// slots bounds concurrent model generations across Ask and AskStream.
	// The per-tenant/per-user token buckets in main.go's aiChain bound request
	// *rate*, not how many slow generations are in flight at once.
	slots *aiLimiter
	// slotWait is how long a request queues for a slot (aiSlotWait; a field
	// so tests don't wait out the real value).
	slotWait time.Duration
	// waker starts the self-hosted Ollama box when a request finds it down.
	// Nil when the Fly lifecycle isn't configured (local dev, always-on box).
	waker ollamaWaker
	// aiSettings resolves the platform+tenant on/off switches (30s TTL cache
	// in front of platform_ai_settings/ai_settings), gating Ask/AskStream
	// (via prepareAsk), Reindex, and Warm.
	aiSettings *aisettings.Cache
	// toggleListener is notified after a successful platform-level toggle
	// write. Nil until a later step wires one (Ollama lifecycle, reindex
	// queue pause) via WithPlatformToggleListener.
	toggleListener PlatformToggleListener
	// catchUpNotifier is nudged after a successful tenant-level toggle write
	// that switches AI on, so that tenant's RAG index maintenance loop
	// catches up immediately. Nil until wired via WithCatchUpNotifier.
	catchUpNotifier tenantCatchUpNotifier
	// warmer single-flights POST /api/tenant/ai/warm's detached warm-up.
	warmer *assistantWarmer
	// ollamaState reports the shared Ollama Machine's aggregated state for
	// GetPlatformSettings's ollamaState field. Nil when no Fly lifecycle is
	// configured (local dev) — that field then always reads "unknown".
	ollamaState ollamaStater
	// leaseHolders lists other environments' unexpired holders of the shared
	// Ollama lease, best-effort, for GetPlatformSettings's leaseHolders
	// field. Nil under the same conditions as ollamaState.
	leaseHolders leaseHolderLister
	// helpCache serves intentHelp, first-turn asks from a small in-process
	// LRU (see ai_help_cache.go) — help content is identical for every
	// tenant, so one cache safely serves all of them.
	helpCache *helpCache
}

// ollamaStater is the point-of-use view of *services.OllamaLifecycle.State.
type ollamaStater interface {
	State(ctx context.Context) (string, error)
}

// leaseHolderLister is the point-of-use view of *services.OllamaLease.OtherHolders.
type leaseHolderLister interface {
	OtherHolders(ctx context.Context) []string
}

// WithOllamaStatus wires the shared Ollama Machine's state summary and lease
// holder list onto GetPlatformSettings and returns the receiver for chaining
// at construction in main.go. Unset by default: ollamaState reads "unknown"
// and leaseHolders reads empty, matching no Fly lifecycle configured.
func (h *AIOps) WithOllamaStatus(state ollamaStater, holders leaseHolderLister) *AIOps {
	h.ollamaState = state
	h.leaseHolders = holders
	return h
}

// NewAIOps constructs the handler group. queryEmbed MUST be a query embedder
// and docEmbed a document embedder (ollama.NewQueryEmbedder /
// ollama.NewDocEmbedder) — the two apply different task prefixes, and mixing
// them produces vectors that are not comparable with the stored ones.
func NewAIOps(cpPool *pgxpool.Pool, queryEmbed ragcore.Embedder, llm ragcore.LLMClient, cp platformAdminChecker, docEmbed ragcore.Embedder) *AIOps {
	return &AIOps{
		cpPool: cpPool, queryEmbed: queryEmbed, llm: llm, cp: cp, docEmbed: docEmbed,
		slots: newAILimiter(aiGlobalSlots, aiPerTenantSlots), slotWait: aiSlotWait,
		aiSettings: aisettings.NewCache(nil), warmer: newAssistantWarmer(nil),
		helpCache: newHelpCache(nil),
	}
}

// WithReranker wires an optional cross-encoder reranker (e.g.
// provider/tei.NewReranker) onto every ask this AIOps serves and returns the
// receiver for chaining at construction in main.go. Unset by default —
// reranking is off unless AI_RERANK_BASE_URL is configured.
func (h *AIOps) WithReranker(r ragcore.Reranker, candidateK int) *AIOps {
	h.reranker = r
	h.rerankCandidates = candidateK
	return h
}

// WithOllamaWaker wires the on-demand Ollama start (see withOllamaWake) and
// returns the receiver for chaining at construction in main.go.
func (h *AIOps) WithOllamaWaker(w ollamaWaker) *AIOps {
	h.waker = w
	return h
}

// WithSettingsCache replaces this AIOps's default aisettings.Cache with a
// shared one and returns the receiver for chaining at construction in
// main.go. main.go builds exactly one Cache and passes it both here and to
// the RAG index maintenance loops (rag_indexing.go) — without this, a toggle
// PUT (which invalidates the handler's own cache) would leave the index
// loops reading a stale cached value for up to cacheTTL.
func (h *AIOps) WithSettingsCache(c *aisettings.Cache) *AIOps {
	h.aiSettings = c
	return h
}

// Reindex handles POST /api/tenant/ai/reindex (admin only). Enqueues every
// CRM record for re-embedding (used after an embedding-model change or backfill).
func (h *AIOps) Reindex(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	status, err := h.aiSettings.Status(r.Context(), h.cpPool, pool, tenant.ID)
	if err != nil {
		slog.Error("ai settings status check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !status.Available {
		writeAssistantDisabled(w)
		return
	}

	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "ai_reindex_denied", "tenant_id", tenant.ID)
		fail(w, http.StatusForbidden, "You do not have permission to reindex this workspace.")
		return
	}

	store := crmstore.For(tenant.DesignVersion)
	q := index.NewQueue(pool)
	enqueued := 0
	for _, key := range crmstore.CRMWorkflowKeys() {
		recs, err := store.ListRecords(r.Context(), pool, key, "all", "")
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to list records for reindex.")
			return
		}
		for _, rec := range recs {
			if err := q.Enqueue(r.Context(), rec.ID, "upsert"); err == nil {
				enqueued++
			}
		}
	}

	logSecurityEvent(r, "ai_reindex", "tenant_id", tenant.ID, "enqueued", enqueued)
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "enqueued": enqueued})
}

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

	res, pruned, err := IngestHelpCorpus(r.Context(), h.cpPool, h.docEmbed)
	if err != nil {
		slog.Error("reindex help failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to reindex app-help docs.")
		return
	}

	logSecurityEvent(r, "ai_reindex_help", "ingested", len(res.Ingested), "failed", len(res.Failed), "pruned", pruned)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": res, "pruned": pruned})
}

package controllers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// aiScopeResources are the CRM resources rag_chunks currently covers (see
// crmstore.CRMWorkflowKeys and controllers/crm.go storeFromContext).
var aiScopeResources = []authz.Resource{authz.ResourceLead, authz.ResourceProspect, authz.ResourceCustomer}

// narrowestScope picks the most restrictive scope among a set of granted
// decisions. Retrieval ANDs a single scope clause across every rag_chunks row
// regardless of source workflow, so using the caller's broadest per-resource
// grant would over-expose a workflow they have narrower access to; using the
// narrowest is the safe (if occasionally under-inclusive) choice. A resource
// with no grant is simply excluded, not treated as a reason to deny — e.g. an
// "own" grant on lead still correctly returns zero prospect chunks the caller
// has no grant on at all, since owner_user_id won't match. Returns ("", false)
// only when NONE of the resources are granted.
func narrowestScope(decisions []authz.Decision) (authz.Scope, bool) {
	var narrowest authz.Scope
	granted := false
	for _, d := range decisions {
		if !d.Allowed {
			continue
		}
		if !granted || authz.ScopeRank(d.Scope) < authz.ScopeRank(narrowest) {
			narrowest = d.Scope
			granted = true
		}
	}
	return narrowest, granted
}

// AIOps serves the tenant AI assistant: POST /api/tenant/ai/ask (RAG chat)
// and POST /api/tenant/ai/reindex (admin: re-enqueue every CRM record).
// queryEmbed and llm are injected so tests can substitute ai.FakeEmbedder /
// ai.FakeLLM — no network calls in tests (see the plan's global invariants).
type AIOps struct {
	cpPool     *pgxpool.Pool
	queryEmbed ai.Embedder
	llm        ai.LLMClient
}

// NewAIOps constructs the handler group. queryEmbed MUST apply the
// search_query: prefix (see ai.NewOllamaQueryEmbedder).
func NewAIOps(cpPool *pgxpool.Pool, queryEmbed ai.Embedder, llm ai.LLMClient) *AIOps {
	return &AIOps{cpPool: cpPool, queryEmbed: queryEmbed, llm: llm}
}

type askRequestBody struct {
	Question string `json:"question"`
}

// Ask handles POST /api/tenant/ai/ask. Chain: RequireAuth -> per-tenant rate
// limit -> TenantResolver (via tenantChain in main.go). Scope is resolved
// from the caller's roles and enforced by RagStore.SearchScoped (IDOR-safe).
func (h *AIOps) Ask(w http.ResponseWriter, r *http.Request) {
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

	var body askRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		fail(w, http.StatusBadRequest, "question is required.")
		return
	}

	decisions := make([]authz.Decision, 0, len(aiScopeResources))
	for _, res := range aiScopeResources {
		d, err := authz.Check(r.Context(), pool, payload.ID, res, authz.ActionRead)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return
		}
		decisions = append(decisions, d)
	}
	scope, ok := narrowestScope(decisions)
	if !ok {
		logSecurityEvent(r, "ai_query_denied", "tenant_id", tenant.ID)
		fail(w, http.StatusForbidden, "You do not have permission to query records.")
		return
	}

	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	teamIDs, err := workflow.TeamIDsForUser(r.Context(), pool, callerUserID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve team membership.")
		return
	}

	orch := ai.NewOrchestrator(h.queryEmbed, ai.CombinedRetriever{
		Tenant: ai.NewRagStore(pool),
		Help:   ai.NewCPHelpStore(h.cpPool),
	}, h.llm)
	res, err := orch.Ask(r.Context(), ai.AskRequest{
		Question:     body.Question,
		Scope:        string(scope),
		CallerUserID: callerUserID,
		TeamIDs:      teamIDs,
	})
	if err != nil {
		fail(w, http.StatusBadGateway, "The assistant is temporarily unavailable.")
		return
	}

	logSecurityEvent(r, "ai_query", "tenant_id", tenant.ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": res})
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

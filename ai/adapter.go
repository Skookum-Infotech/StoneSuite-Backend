// Package ai adapts the generic go-rag library to StoneSuite: it translates
// StoneSuite's RBAC scope vocabulary into a rag.ScopeFilter, assembles the two
// corpora an assistant question searches, and carries StoneSuite's own prompt.
//
// The retrieval engine itself lives in github.com/Skookum-Infotech/go-rag and
// knows nothing about tenants, workflows, or permissions. Everything in this
// package is the part that could not be generic.
package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/Skookum-Infotech/go-rag/store/pgvector"
)

// Table and corpus names. The corpus name becomes Citation.SourceType, which
// the frontend switches on, so these strings are part of the API contract.
const (
	recordsTable = "rag_chunks"
	helpTable    = "cp_rag_chunks"

	// CorpusRecords is the tenant's own CRM records — scoped per caller.
	CorpusRecords = "record"
	// CorpusHelp is app documentation — identical for every tenant.
	CorpusHelp = "help"
)

// Retrieval budgets and the relevance floor.
//
// The budgets stay modest rather than wide-and-let-the-model-sort-it-out: the
// chat model is a small self-hosted one on a CPU-bound box, so every extra
// citation is more prefill before generation starts, and the top matches are
// already the most relevant. The floor is conservative; revisit it once the
// eval harness can measure refusals against hallucination on weak matches.
const (
	recordsK  = 4
	helpK     = 2
	floorDist = 0.9
)

// refusalPhrase is the exact string the prompt instructs the model to use when
// it has no grounding, and the string the refusal-rate metric detects. Keep it
// and systemPrompt in sync — they are passed to WithPrompt together for
// exactly that reason.
const refusalPhrase = rag.DefaultRefusalPhrase

const systemPrompt = `You are StoneSuite's assistant. Answer ONLY using the provided context.
If the answer is not in the context, say "` + refusalPhrase + `" Cite sources by their [n] markers. Never invent data.`

// ScopeFilter translates a StoneSuite RBAC scope into the SQL predicate that
// narrows rag_chunks to what the caller may read.
//
// The two-level model is fail-closed by construction: only the scopes named
// here widen anything, and everything else — an empty string, a typo, or the
// retired "team" grant still sitting in a role_permissions row — falls to the
// default and denies. That is deliberate. A scope this code does not recognise
// is a scope it cannot reason about, and denying is the only safe response.
//
// callerUserID is bound as a query parameter, never interpolated.
func ScopeFilter(scope, callerUserID string) rag.ScopeFilter {
	switch scope {
	case "own":
		return func(firstArg int) (string, []any) {
			return fmt.Sprintf("owner_user_id = $%d", firstArg), []any{callerUserID}
		}
	case "all":
		return rag.AllowAll
	default:
		return rag.DenyAll
	}
}

// RecordsCorpus builds the tenant record corpus for one caller. The scope is
// fixed here, at construction, so the returned corpus cannot be used to reach
// another caller's records.
func RecordsCorpus(tenantPool *pgxpool.Pool, scope, callerUserID string) *pgvector.Corpus {
	return pgvector.New(tenantPool, pgvector.Options{
		Table: recordsTable,
		Name:  CorpusRecords,
		Scope: ScopeFilter(scope, callerUserID),
	})
}

// HelpCorpus builds the app-help corpus over the control-plane pool.
//
// AllowAll is passed explicitly rather than left nil: help content is identical
// for every tenant and is nobody's private data, so it genuinely has no
// per-caller restriction — and saying so at the call site distinguishes that
// from having forgotten to work out the scope.
func HelpCorpus(cpPool *pgxpool.Pool) *pgvector.Corpus {
	return pgvector.New(cpPool, pgvector.Options{
		Table:    helpTable,
		Name:     CorpusHelp,
		IDColumn: "section",
		Scope:    rag.AllowAll,
	})
}

// AskRequest carries a question plus the caller's resolved identity and scope.
// The controller resolves scope from the RBAC enforcer; nothing downstream —
// and in particular nothing the model produces — can influence it.
type AskRequest struct {
	Question     string
	Scope        string
	CallerUserID string
	// History is prior turns of the caller's conversation, oldest first —
	// see ConversationStore.History. Optional: nil behaves exactly as a
	// single-turn ask always has.
	History []rag.Message
}

// Assistant builds a per-caller orchestrator over StoneSuite's corpora.
//
// Held per request rather than per process because the record corpus is scoped
// to one caller. The pools are shared and the corpora are thin values over
// them, so assembly is cheap; what it buys is that a caller's scope is fixed at
// construction and cannot drift between retrieval arms.
type Assistant struct {
	tenantPool       *pgxpool.Pool
	cpPool           *pgxpool.Pool
	queryEmbed       rag.Embedder
	llm              rag.LLMClient
	metrics          rag.Metrics
	reranker         rag.Reranker
	rerankCandidates int
}

// NewAssistant wires the shared dependencies. queryEmbed MUST be a query
// embedder — an embedder built with the document prefix produces vectors that
// are not comparable with the stored ones.
func NewAssistant(tenantPool, cpPool *pgxpool.Pool, queryEmbed rag.Embedder, llm rag.LLMClient) *Assistant {
	return &Assistant{tenantPool: tenantPool, cpPool: cpPool, queryEmbed: queryEmbed, llm: llm}
}

// WithMetrics wires an instrumentation sink onto every orchestrator this
// Assistant builds.
func (a *Assistant) WithMetrics(m rag.Metrics) *Assistant {
	a.metrics = m
	return a
}

// WithReranker wires an optional cross-encoder reranker (e.g.
// provider/tei.NewReranker) onto every orchestrator this Assistant builds.
// candidateK is how wide each corpus retrieves before the reranker narrows
// back down to its normal K (see rag.CorpusConfig.RerankK) — a candidateK at
// or below that corpus's K is a no-op widen, not an error.
//
// Optional: an Assistant without one behaves exactly as before Phase 2 —
// RerankK is inert with no configured Reranker (see rag.Orchestrator).
func (a *Assistant) WithReranker(r rag.Reranker, candidateK int) *Assistant {
	a.reranker = r
	a.rerankCandidates = candidateK
	return a
}

// ForCaller assembles the orchestrator for one caller: their scoped records
// plus shared help, in that order, so citation [1] is the first record hit.
func (a *Assistant) ForCaller(scope, callerUserID string) *rag.Orchestrator {
	corpora := []rag.CorpusConfig{
		{Corpus: RecordsCorpus(a.tenantPool, scope, callerUserID), K: recordsK, FloorDistance: floorDist, RerankK: a.rerankCandidates},
		{Corpus: HelpCorpus(a.cpPool), K: helpK, FloorDistance: floorDist, RerankK: a.rerankCandidates},
	}
	o := rag.NewOrchestrator(a.queryEmbed, a.llm, corpora).WithPrompt(systemPrompt, refusalPhrase)
	if a.metrics != nil {
		o = o.WithMetrics(a.metrics)
	}
	if a.reranker != nil {
		o = o.WithReranker(a.reranker)
	}
	return o
}

// Ask answers one question within the caller's scope.
func (a *Assistant) Ask(ctx context.Context, req AskRequest) (rag.AskResult, error) {
	return a.ForCaller(req.Scope, req.CallerUserID).Ask(ctx, rag.AskRequest{Question: req.Question, History: req.History})
}

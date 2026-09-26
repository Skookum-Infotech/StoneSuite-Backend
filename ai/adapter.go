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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/provider/ollama"
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

// Intent is the caller's classified question type (see
// controllers.classifyIntent), which ForCaller uses to pick which corpus (or
// corpora), how wide to search each, and which prompt/token budget to use.
// Defined here rather than in controllers so the ai package stays the single
// source of truth for what an intent actually changes about retrieval.
type Intent string

const (
	// IntentHelp is a question about how to use StoneSuite itself — the
	// records corpus is never searched, even when the caller can read
	// records, so a help question never spends its budget on record chunks.
	IntentHelp Intent = "help"
	// IntentData is a question about the caller's own CRM data — the help
	// corpus is never searched.
	IntentData Intent = "data"
	// IntentMixed searches both corpora at a narrower K each — used when the
	// question carries cues for both, or neither.
	IntentMixed Intent = "mixed"
)

// Retrieval budgets and the relevance floor, per intent/corpus.
//
// The budgets stay modest rather than wide-and-let-the-model-sort-it-out: the
// chat model is a small self-hosted one on a CPU-bound box, so every extra
// citation is more prefill before generation starts, and the top matches are
// already the most relevant. A single-corpus intent (help or data) searches
// that corpus a little wider than a mixed ask splits between two, since it
// has no second corpus to also draw from.
const (
	helpOnlyK  = 3
	dataOnlyK  = 3
	mixedEachK = 2

	// recordsFloorDist and helpFloorDist are the per-corpus relevance floors
	// (see rag.CorpusConfig.FloorDistance): a vector hit farther than this
	// cosine distance is dropped before it ever reaches the model. Provisional
	// — tightened from the single floorDist=0.9 that filtered effectively
	// nothing — revisit both once the e2e eval harness (chore/ai-e2e-harness)
	// has logged enough real distances to calibrate against actual
	// refusal/hallucination rates rather than a guess.
	recordsFloorDist = 0.55
	helpFloorDist    = 0.6
)

// Per-intent output token budgets: the num_predict cap passed to
// WithMaxTokens (see maxTokensLLM) and to WithBudget's maxPredictTokens, sized
// to what each answer shape actually needs — a direct data answer is one
// sentence plus a short supporter, a help answer walks through screens/menus,
// a mixed answer may need to do a bit of both. Smaller than
// rag.DefaultMaxPredictTokens across the board: on a CPU-bound ~20-25
// tok/s box, every unused token of headroom is generation time nobody asked
// for.
const (
	dataMaxTokens  = 200
	helpMaxTokens  = 300
	mixedMaxTokens = 250
)

// recordsEFSearch widens the HNSW candidate set for the scoped records query.
// HNSW filters AFTER the index scan, so a caller whose grants cover a small
// slice of a large tenant (few "own" rows) can otherwise get fewer than K
// results, or none, at pgvector's default of 40.
const recordsEFSearch = 100

// helpIDColumn makes a help citation's id "<doc> › <section>" rather than the
// bare section title: two docs each with an "Overview" section otherwise share
// one id, and fusion dedupes on id, silently dropping one of them.
const helpIDColumn = `doc_key || ' › ' || section`

// RecordsCorpus builds the tenant record corpus for one caller. The scope is
// fixed here, at construction, so the returned corpus cannot be used to reach
// another caller's records — or record types they hold no grant on.
func RecordsCorpus(tenantPool *pgxpool.Pool, grants Grants, callerUserID string) *pgvector.Corpus {
	return pgvector.New(tenantPool, pgvector.Options{
		Table:    recordsTable,
		Name:     CorpusRecords,
		Scope:    RecordScopeFilter(grants, callerUserID),
		EFSearch: recordsEFSearch,
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
		IDColumn: helpIDColumn,
		Scope:    rag.AllowAll,
	})
}

// AskRequest carries a question plus the caller's resolved identity and scope.
// The controller resolves scope from the RBAC enforcer; nothing downstream —
// and in particular nothing the model produces — can influence it.
type AskRequest struct {
	Question string
	// Grants are the record types the caller may read and their scope for
	// each. Empty means help-docs-only: the records corpus is not searched.
	Grants       Grants
	CallerUserID string
	// Intent picks the corpus/corpora, K, prompt and token budget ForCaller
	// uses — see controllers.classifyIntent. Zero value behaves as IntentMixed.
	Intent Intent
	// History is prior turns of the caller's conversation, oldest first —
	// see ConversationStore.History. Optional: nil behaves exactly as a
	// single-turn ask always has.
	History []rag.Message
}

// maxTokensLLM is the point-of-use view of an LLMClient that can bound its
// own per-call token budget — e.g. provider/ollama.LLMClient.WithMaxTokens.
// Optional: an llm without this capability is used unbounded for every
// intent (see Assistant.llmFor).
type maxTokensLLM interface {
	WithMaxTokens(n int) rag.LLMClient
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
	// dataLLM/helpLLM/mixedLLM are llm bounded to each intent's own
	// WithMaxTokens budget (see maxTokensLLM), computed once here at
	// construction rather than per ask — WithMaxTokens is a cheap value copy,
	// but there is still no reason to repeat it on every retrieval. Equal to
	// llm unchanged when llm doesn't implement maxTokensLLM.
	dataLLM, helpLLM, mixedLLM rag.LLMClient
}

// NewAssistant wires the shared dependencies. queryEmbed MUST be a query
// embedder — an embedder built with the document prefix produces vectors that
// are not comparable with the stored ones.
func NewAssistant(tenantPool, cpPool *pgxpool.Pool, queryEmbed rag.Embedder, llm rag.LLMClient) *Assistant {
	a := &Assistant{
		tenantPool: tenantPool, cpPool: cpPool, queryEmbed: queryEmbed, llm: llm,
		dataLLM: llm, helpLLM: llm, mixedLLM: llm,
	}
	if setter, ok := llm.(maxTokensLLM); ok {
		a.dataLLM = setter.WithMaxTokens(dataMaxTokens)
		a.helpLLM = setter.WithMaxTokens(helpMaxTokens)
		a.mixedLLM = setter.WithMaxTokens(mixedMaxTokens)
	}
	return a
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

// promptFor returns the system prompt, bounded LLM client, and max-predict
// token budget for in — the three things that change together per intent, so
// callers can never mismatch a prompt with the wrong budget.
func (a *Assistant) promptFor(in Intent) (prompt string, llm rag.LLMClient, maxTokens int) {
	switch in {
	case IntentData:
		return dataPrompt, a.dataLLM, dataMaxTokens
	case IntentHelp:
		return helpPrompt, a.helpLLM, helpMaxTokens
	default: // IntentMixed and any unrecognized value
		return mixedPrompt, a.mixedLLM, mixedMaxTokens
	}
}

// ForCaller assembles the orchestrator for one caller and one classified
// intent. A caller with no readable record type always gets IntentHelp,
// regardless of what was classified — the records corpus is left out
// entirely rather than searched under a deny-all filter, and a help-only
// caller's questions have nothing else to search anyway.
func (a *Assistant) ForCaller(grants Grants, callerUserID string, in Intent) *rag.Orchestrator {
	hasRecords := len(grants.Types()) > 0
	effIntent := in
	if !hasRecords {
		effIntent = IntentHelp
	}

	var corpora []rag.CorpusConfig
	switch effIntent {
	case IntentData:
		corpora = append(corpora, rag.CorpusConfig{Corpus: RecordsCorpus(a.tenantPool, grants, callerUserID), K: dataOnlyK, FloorDistance: recordsFloorDist, RerankK: a.rerankCandidates})
	case IntentHelp:
		corpora = append(corpora, rag.CorpusConfig{Corpus: HelpCorpus(a.cpPool), K: helpOnlyK, FloorDistance: helpFloorDist, RerankK: a.rerankCandidates})
	default: // IntentMixed
		if hasRecords {
			corpora = append(corpora, rag.CorpusConfig{Corpus: RecordsCorpus(a.tenantPool, grants, callerUserID), K: mixedEachK, FloorDistance: recordsFloorDist, RerankK: a.rerankCandidates})
		}
		corpora = append(corpora, rag.CorpusConfig{Corpus: HelpCorpus(a.cpPool), K: mixedEachK, FloorDistance: helpFloorDist, RerankK: a.rerankCandidates})
	}

	prompt, llm, maxTokens := a.promptFor(effIntent)
	o := rag.NewOrchestrator(a.queryEmbed, llm, corpora).
		WithPrompt(prompt, refusalPhrase).
		WithBudget(ollama.DefaultContextWindow, maxTokens)
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
	return a.ForCaller(req.Grants, req.CallerUserID, req.Intent).Ask(ctx, rag.AskRequest{Question: req.Question, History: req.History})
}

// AskStream is Ask's streaming twin: identical scope/corpora/prompt, but the
// reply is delivered to sink token-by-token as it's generated — see
// rag.Orchestrator.AskStream.
func (a *Assistant) AskStream(ctx context.Context, req AskRequest, sink rag.StreamSink) (rag.AskResult, error) {
	return a.ForCaller(req.Grants, req.CallerUserID, req.Intent).AskStream(ctx, rag.AskRequest{Question: req.Question, History: req.History}, sink)
}

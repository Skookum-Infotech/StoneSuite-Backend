package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// refusalPhrase is the exact string the system prompt instructs the model to
// answer with when it has no grounding — also the refusal-rate metric's
// detection string (ObserveAsk), so keep this and the prompt text in sync.
const refusalPhrase = "I don't have that information."

const systemPrompt = `You are StoneSuite's assistant. Answer ONLY using the provided context.
If the answer is not in the context, say "` + refusalPhrase + `" Cite sources by their [n] markers. Never invent data.`

// Retriever is the read side the orchestrator depends on: RBAC-scoped tenant
// record chunks plus unscoped app-help chunks, each with a vector (semantic)
// and lexical (keyword, full-text) search arm — see ai/fuse.go for how the
// two ranked lists are fused into one.
type Retriever interface {
	SearchScoped(ctx context.Context, queryVec []float32, scope, callerUserID string, k int) ([]Citation, error)
	SearchScopedLexical(ctx context.Context, queryText, scope, callerUserID string, k int) ([]Citation, error)
	SearchHelp(ctx context.Context, queryVec []float32, k int) ([]Citation, error)
	SearchHelpLexical(ctx context.Context, queryText string, k int) ([]Citation, error)
}

// Compile-time proof CombinedRetriever satisfies Retriever.
var _ Retriever = CombinedRetriever{}

// CombinedRetriever composes a tenant RagStore with a control-plane
// CPHelpStore into the single Retriever the Orchestrator depends on.
type CombinedRetriever struct {
	Tenant *RagStore
	Help   *CPHelpStore
}

// SearchScoped delegates to the tenant store.
func (c CombinedRetriever) SearchScoped(ctx context.Context, queryVec []float32, scope, callerUserID string, k int) ([]Citation, error) {
	return c.Tenant.SearchScoped(ctx, queryVec, scope, callerUserID, k)
}

// SearchScopedLexical delegates to the tenant store's full-text search.
func (c CombinedRetriever) SearchScopedLexical(ctx context.Context, queryText, scope, callerUserID string, k int) ([]Citation, error) {
	return c.Tenant.SearchScopedLexical(ctx, queryText, scope, callerUserID, k)
}

// SearchHelp delegates to the control-plane help store.
func (c CombinedRetriever) SearchHelp(ctx context.Context, queryVec []float32, k int) ([]Citation, error) {
	return c.Help.Search(ctx, queryVec, k)
}

// SearchHelpLexical delegates to the control-plane help store's full-text search.
func (c CombinedRetriever) SearchHelpLexical(ctx context.Context, queryText string, k int) ([]Citation, error) {
	return c.Help.SearchLexical(ctx, queryText, k)
}

// AskRequest carries the question + the caller's resolved RBAC scope.
type AskRequest struct {
	Question     string
	Scope        string
	CallerUserID string
}

// AskResult is the grounded answer + its citations.
type AskResult struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
}

// Orchestrator runs the RAG pipeline behind one method.
type Orchestrator struct {
	emb     Embedder // a query embedder (search_query: prefix)
	ret     Retriever
	llm     LLMClient
	metrics Metrics
}

// NewOrchestrator wires the pipeline. emb MUST be a query embedder. Built
// with a no-op Metrics sink — call WithMetrics to record instrumentation.
func NewOrchestrator(emb Embedder, ret Retriever, llm LLMClient) *Orchestrator {
	return &Orchestrator{emb: emb, ret: ret, llm: llm, metrics: noopMetrics{}}
}

// WithMetrics wires an AI-specific metrics sink (e.g. a metrics.AI{} backed
// by Prometheus) and returns the Orchestrator for chaining at construction.
// Optional — an Orchestrator built without this call records nothing.
func (o *Orchestrator) WithMetrics(m Metrics) *Orchestrator {
	o.metrics = m
	return o
}

// tenantRetrievalK and helpRetrievalK cap how many chunks of each kind ground
// one answer. Kept modest (rather than wide-and-let-the-model-sort-it-out)
// because the chat model is a small self-hosted one on a CPU-bound box (see
// ai/ollama_llm.go): every extra citation is more prefill time before
// generation even starts, and the top-k similarity matches are already the
// most relevant ones — a narrower, more targeted prompt serves a small model
// better than a wide one it's too weak to reason over quickly anyway.
const (
	tenantRetrievalK = 4
	helpRetrievalK   = 2
)

// relevanceFloorDistance is the maximum acceptable pgvector cosine distance
// (0 = identical, larger = less similar) for a vector-arm citation to count
// as genuinely relevant grounding. Chosen conservatively; revisit once the
// golden eval set can measure the tradeoff between false "I don't have that
// information" answers and hallucination on weak matches.
const relevanceFloorDistance = 0.9

// hasRelevantMatch reports whether cites contains at least one citation worth
// grounding an answer in: any lexical (full-text) hit — a literal term match
// needs no distance floor — or any vector hit at or under the relevance
// floor. Without this, buildScopedSearch's lack of a similarity floor means
// top-k always returns k chunks regardless of true relevance, handing a weak
// model noise it may hallucinate over instead of correctly saying it doesn't
// know.
func hasRelevantMatch(cites []Citation) bool {
	for _, c := range cites {
		if !c.DistanceValid || c.Distance <= relevanceFloorDistance {
			return true
		}
	}
	return false
}

// Ask embeds the question, retrieves scoped tenant chunks + app-help, and asks
// the LLM to answer strictly from that context.
func (o *Orchestrator) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	embedStart := time.Now()
	vecs, err := o.emb.Embed(ctx, []string{req.Question})
	o.metrics.ObserveEmbed(time.Since(embedStart).Seconds())
	if err != nil {
		return AskResult{}, fmt.Errorf("embed question: %w", err)
	}
	qv := vecs[0]

	// Vector arm: fatal on error, as before.
	tVec, err := o.ret.SearchScoped(ctx, qv, req.Scope, req.CallerUserID, tenantRetrievalK)
	if err != nil {
		return AskResult{}, fmt.Errorf("retrieve: %w", err)
	}
	// Lexical arm: the ONE intentional non-fatal path — a full-text search
	// hiccup degrades to vector-only retrieval instead of 502ing the whole
	// assistant, since the vector arm alone is what today's behavior already is.
	tLex, lerr := o.ret.SearchScopedLexical(ctx, req.Question, req.Scope, req.CallerUserID, tenantRetrievalK)
	if lerr != nil {
		slog.Warn("lexical tenant search failed; using vector-only", "err", lerr)
		tLex = nil
	}
	tenantCites := fuseRRF(tenantRetrievalK, tVec, tLex)

	hVec, err := o.ret.SearchHelp(ctx, qv, helpRetrievalK)
	if err != nil {
		return AskResult{}, fmt.Errorf("retrieve help: %w", err)
	}
	hLex, lerr := o.ret.SearchHelpLexical(ctx, req.Question, helpRetrievalK)
	if lerr != nil {
		slog.Warn("lexical help search failed; using vector-only", "err", lerr)
		hLex = nil
	}
	helpCites := fuseRRF(helpRetrievalK, hVec, hLex)

	// Relevance floor: drop an arm's results entirely if none of them clear
	// the bar (see hasRelevantMatch) rather than grounding the model in noise
	// it's likely to hallucinate over. Applied per-arm so a genuinely
	// relevant help match, say, still grounds the answer even when nothing
	// relevant was found in the tenant's own records, and vice versa.
	if !hasRelevantMatch(tenantCites) {
		tenantCites = nil
	}
	if !hasRelevantMatch(helpCites) {
		helpCites = nil
	}
	cites := append(tenantCites, helpCites...)

	var b strings.Builder
	for i, c := range cites {
		fmt.Fprintf(&b, "[%d] (%s) %s\n", i+1, c.SourceType, c.Content)
	}
	msg := fmt.Sprintf("Context:\n%s\nQuestion: %s", b.String(), req.Question)
	llmStart := time.Now()
	answer, err := o.llm.Chat(ctx, systemPrompt, []Message{{Role: "user", Content: msg}})
	o.metrics.ObserveLLM(time.Since(llmStart).Seconds(), errors.Is(err, context.DeadlineExceeded))
	if err != nil {
		return AskResult{}, fmt.Errorf("llm: %w", err)
	}
	o.metrics.ObserveAsk(strings.Contains(answer, refusalPhrase))
	return AskResult{Answer: answer, Citations: citedOnly(cites, answer)}, nil
}

// citationMarkerRe matches the [n] source markers the system prompt
// instructs the LLM to cite with.
var citationMarkerRe = regexp.MustCompile(`\[(\d+)\]`)

// citedOnly filters cites down to the ones the answer actually references via
// a [n] marker (n is the 1-based position in cites), preserving cites' order.
// Without this, the client would show every retrieved chunk as "referenced"
// even ones the model saw but didn't use — misleading at low record counts,
// where top-k retrieval returns the tenant's whole record set regardless of
// relevance (see buildScopedSearch: no similarity floor).
func citedOnly(cites []Citation, answer string) []Citation {
	cited := make(map[int]bool)
	for _, m := range citationMarkerRe.FindAllStringSubmatch(answer, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			cited[n] = true
		}
	}
	out := []Citation{}
	for i, c := range cites {
		if cited[i+1] {
			out = append(out, c)
		}
	}
	return out
}

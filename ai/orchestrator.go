package ai

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const systemPrompt = `You are StoneSuite's assistant. Answer ONLY using the provided context.
If the answer is not in the context, say "I don't have that information." Cite sources by their [n] markers. Never invent data.`

// Retriever is the read side the orchestrator depends on: RBAC-scoped tenant
// record chunks plus unscoped app-help chunks.
type Retriever interface {
	SearchScoped(ctx context.Context, queryVec []float32, scope, callerUserID string, teamIDs []string, k int) ([]Citation, error)
	SearchHelp(ctx context.Context, queryVec []float32, k int) ([]Citation, error)
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
func (c CombinedRetriever) SearchScoped(ctx context.Context, queryVec []float32, scope, callerUserID string, teamIDs []string, k int) ([]Citation, error) {
	return c.Tenant.SearchScoped(ctx, queryVec, scope, callerUserID, teamIDs, k)
}

// SearchHelp delegates to the control-plane help store.
func (c CombinedRetriever) SearchHelp(ctx context.Context, queryVec []float32, k int) ([]Citation, error) {
	return c.Help.Search(ctx, queryVec, k)
}

// AskRequest carries the question + the caller's resolved RBAC scope.
type AskRequest struct {
	Question     string
	Scope        string
	CallerUserID string
	TeamIDs      []string
}

// AskResult is the grounded answer + its citations.
type AskResult struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
}

// Orchestrator runs the RAG pipeline behind one method.
type Orchestrator struct {
	emb Embedder // a query embedder (search_query: prefix)
	ret Retriever
	llm LLMClient
}

// NewOrchestrator wires the pipeline. emb MUST be a query embedder.
func NewOrchestrator(emb Embedder, ret Retriever, llm LLMClient) *Orchestrator {
	return &Orchestrator{emb: emb, ret: ret, llm: llm}
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

// Ask embeds the question, retrieves scoped tenant chunks + app-help, and asks
// the LLM to answer strictly from that context.
func (o *Orchestrator) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	vecs, err := o.emb.Embed(ctx, []string{req.Question})
	if err != nil {
		return AskResult{}, fmt.Errorf("embed question: %w", err)
	}
	qv := vecs[0]
	tenantCites, err := o.ret.SearchScoped(ctx, qv, req.Scope, req.CallerUserID, req.TeamIDs, tenantRetrievalK)
	if err != nil {
		return AskResult{}, fmt.Errorf("retrieve: %w", err)
	}
	helpCites, err := o.ret.SearchHelp(ctx, qv, helpRetrievalK)
	if err != nil {
		return AskResult{}, fmt.Errorf("retrieve help: %w", err)
	}
	cites := append(tenantCites, helpCites...)

	var b strings.Builder
	for i, c := range cites {
		fmt.Fprintf(&b, "[%d] (%s) %s\n", i+1, c.SourceType, c.Content)
	}
	msg := fmt.Sprintf("Context:\n%s\nQuestion: %s", b.String(), req.Question)
	answer, err := o.llm.Chat(ctx, systemPrompt, []Message{{Role: "user", Content: msg}})
	if err != nil {
		return AskResult{}, fmt.Errorf("llm: %w", err)
	}
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

package rag

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

// DefaultRefusalPhrase is the exact string the default system prompt instructs
// the model to answer with when it has no grounding. It doubles as the
// refusal-rate metric's detection string (Metrics.ObserveAsk), so a custom
// prompt and its refusal phrase must be set together — see WithPrompt.
const DefaultRefusalPhrase = "I don't have that information."

// DefaultSystemPrompt builds the grounding instruction around a refusal phrase.
// Callers with their own voice should substitute their own prompt, keeping the
// three constraints intact: answer only from context, refuse with the exact
// phrase, cite with [n] markers.
func DefaultSystemPrompt(refusalPhrase string) string {
	return `You are a helpful assistant. Answer ONLY using the provided context.
If the answer is not in the context, say "` + refusalPhrase + `" Cite sources by their [n] markers. Never invent data.`
}

// AskRequest carries one question. Scope is deliberately absent: the caller
// binds it into the corpora it constructs (see Corpus), so a question can only
// ever reach data its asker is permitted to see.
type AskRequest struct {
	Question string
	// History is prior turns of a multi-turn conversation, oldest first —
	// plain question/answer text only, never citations. Retrieval always
	// re-runs fresh for the CURRENT question under the corpora's current
	// scope (Ask never retrieves for a History turn), so replaying stored
	// history can never surface data a since-revoked permission would now
	// deny — only a past *answer*'s own text could still quote something,
	// which is the caller's retention policy to manage, not this package's.
	// Optional: nil behaves exactly as a single-turn ask always has.
	History []Message
}

// AskResult is the grounded answer plus the citations it actually referenced.
type AskResult struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
}

// Orchestrator runs the RAG pipeline behind one method: embed the question,
// search every configured corpus on both arms, fuse and floor each corpus's
// results, then ask the model to answer strictly from what survived.
type Orchestrator struct {
	emb           Embedder // a QUERY embedder — see Fingerprinter
	llm           LLMClient
	corpora       []CorpusConfig
	systemPrompt  string
	refusalPhrase string
	metrics       Metrics
	reranker      Reranker
}

// NewOrchestrator wires the pipeline. emb MUST be a query embedder; corpora are
// searched, and their citations concatenated, in the order given. Built with a
// no-op metrics sink and the default prompt.
func NewOrchestrator(emb Embedder, llm LLMClient, corpora []CorpusConfig) *Orchestrator {
	return &Orchestrator{
		emb:           emb,
		llm:           llm,
		corpora:       corpora,
		systemPrompt:  DefaultSystemPrompt(DefaultRefusalPhrase),
		refusalPhrase: DefaultRefusalPhrase,
		metrics:       noopMetrics{},
	}
}

// WithMetrics wires an instrumentation sink and returns the Orchestrator for
// chaining. Optional — one built without it records nothing.
func (o *Orchestrator) WithMetrics(m Metrics) *Orchestrator {
	o.metrics = m
	return o
}

// WithReranker wires an optional cross-encoder reranker and returns the
// Orchestrator for chaining. Without one, retrieval stays exactly RRF-fused
// order at each corpus's K — see CorpusConfig.RerankK for how a corpus opts
// into the widen-then-rerank behavior this enables.
func (o *Orchestrator) WithReranker(r Reranker) *Orchestrator {
	o.reranker = r
	return o
}

// WithPrompt overrides the system prompt and the refusal phrase together.
//
// They are one call rather than two because they must agree: the phrase is
// what the prompt tells the model to say when it cannot answer, AND what the
// refusal-rate metric looks for in the response. Setting one without the other
// silently zeroes that metric while everything appears to work.
func (o *Orchestrator) WithPrompt(systemPrompt, refusalPhrase string) *Orchestrator {
	o.systemPrompt = systemPrompt
	o.refusalPhrase = refusalPhrase
	return o
}

// hasRelevantMatch reports whether cites holds at least one citation worth
// grounding an answer in: any lexical hit — a literal term match needs no
// distance floor — or any vector hit at or under floor. Without this, a top-k
// search always returns k chunks regardless of true relevance, handing a weak
// model noise it may hallucinate over instead of correctly saying it does not
// know. A floor of 0 accepts everything.
func hasRelevantMatch(cites []Citation, floor float64) bool {
	if floor <= 0 {
		return len(cites) > 0
	}
	for _, c := range cites {
		if !c.DistanceValid || c.Distance <= floor {
			return true
		}
	}
	return false
}

// searchCorpus runs both retrieval arms over one corpus and returns the fused,
// floored result.
//
// The two arms fail differently, on purpose. A vector-arm error is fatal: it is
// the primary retrieval path, and answering without it would quietly downgrade
// the answer's grounding. A lexical-arm error is not: full-text search is an
// enhancement over what vector-only retrieval already delivers, so a hiccup
// there degrades to vector-only rather than failing the whole request.
func (o *Orchestrator) searchCorpus(ctx context.Context, cc CorpusConfig, queryVec []float32, question string) ([]Citation, error) {
	name := cc.Corpus.Name()

	// Retrieve wide only when there's a reranker to narrow it back down —
	// otherwise this is cc.K, identical to pre-reranking behavior.
	retrieveK := cc.K
	if o.reranker != nil && cc.RerankK > cc.K {
		retrieveK = cc.RerankK
	}

	vec, err := cc.Corpus.SearchVector(ctx, queryVec, retrieveK)
	if err != nil {
		return nil, fmt.Errorf("retrieve %s: %w", name, err)
	}

	lex, lerr := cc.Corpus.SearchLexical(ctx, question, retrieveK)
	if lerr != nil {
		slog.Warn("lexical search failed; using vector-only", "corpus", name, "err", lerr)
		lex = nil
	}

	fused := fuseRRF(retrieveK, vec, lex)
	if o.reranker != nil && len(fused) > 0 {
		reranked, rerr := o.reranker.Rerank(ctx, question, fused, cc.K)
		if rerr != nil {
			// Degrade to RRF order, never fail the ask over an accuracy upgrade.
			slog.Warn("rerank failed; falling back to RRF order", "corpus", name, "err", rerr)
		} else {
			fused = reranked
		}
	}
	if len(fused) > cc.K {
		fused = fused[:cc.K]
	}
	if !hasRelevantMatch(fused, cc.FloorDistance) {
		// Drop this corpus entirely rather than grounding on noise. Applied
		// per corpus so a genuinely relevant help match still grounds an
		// answer when nothing relevant was found in the caller's own records,
		// and vice versa.
		return nil, nil
	}
	return fused, nil
}

// Ask embeds the question, retrieves from every corpus, and asks the LLM to
// answer strictly from that context.
func (o *Orchestrator) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	embedStart := time.Now()
	vecs, err := o.emb.Embed(ctx, []string{req.Question})
	o.metrics.ObserveEmbed(time.Since(embedStart).Seconds())
	if err != nil {
		return AskResult{}, fmt.Errorf("embed question: %w", err)
	}
	queryVec := vecs[0]

	var cites []Citation
	for _, cc := range o.corpora {
		found, err := o.searchCorpus(ctx, cc, queryVec, req.Question)
		if err != nil {
			return AskResult{}, err
		}
		cites = append(cites, found...)
	}

	var b strings.Builder
	for i, c := range cites {
		fmt.Fprintf(&b, "[%d] (%s) %s\n", i+1, c.SourceType, c.Content)
	}
	msg := fmt.Sprintf("Context:\n%s\nQuestion: %s", b.String(), req.Question)

	messages := make([]Message, 0, len(req.History)+1)
	messages = append(messages, req.History...)
	messages = append(messages, Message{Role: "user", Content: msg})

	llmStart := time.Now()
	answer, err := o.llm.Chat(ctx, o.systemPrompt, messages)
	o.metrics.ObserveLLM(time.Since(llmStart).Seconds(), errors.Is(err, context.DeadlineExceeded))
	if err != nil {
		return AskResult{}, fmt.Errorf("llm: %w", err)
	}
	o.metrics.ObserveAsk(strings.Contains(answer, o.refusalPhrase))
	return AskResult{Answer: answer, Citations: citedOnly(cites, answer)}, nil
}

// citationMarkerRe matches the [n] source markers the system prompt instructs
// the LLM to cite with.
var citationMarkerRe = regexp.MustCompile(`\[(\d+)\]`)

// citedOnly filters cites down to the ones the answer actually references via a
// [n] marker (n is the 1-based position in cites), preserving order. Without
// this the client would show every retrieved chunk as "referenced", including
// ones the model saw and ignored — misleading at low record counts, where top-k
// returns nearly everything regardless of relevance.
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

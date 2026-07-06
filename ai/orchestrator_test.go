package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

var errBoom = errors.New("boom")

// fakeMetrics records every Observe* call for assertion, so tests can verify
// Orchestrator.Ask instruments the pipeline without a real Prometheus registry.
type fakeMetrics struct {
	embedCalls int
	llmSeconds []float64
	llmTimeout []bool
	askRefused []bool
}

func (f *fakeMetrics) ObserveEmbed(float64) { f.embedCalls++ }
func (f *fakeMetrics) ObserveLLM(seconds float64, timedOut bool) {
	f.llmSeconds = append(f.llmSeconds, seconds)
	f.llmTimeout = append(f.llmTimeout, timedOut)
}
func (f *fakeMetrics) ObserveAsk(refused bool) { f.askRefused = append(f.askRefused, refused) }

type fakeRetriever struct {
	tenant    []Citation
	help      []Citation
	err       error
	tenantLex []Citation // lexical-arm results; nil by default => vector-only behavior
	helpLex   []Citation // lexical-arm results; nil by default => vector-only behavior
}

func (f *fakeRetriever) SearchScoped(_ context.Context, _ []float32, _, _ string, _ []string, _ int) ([]Citation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tenant, nil
}

func (f *fakeRetriever) SearchScopedLexical(_ context.Context, _, _, _ string, _ []string, _ int) ([]Citation, error) {
	return f.tenantLex, nil
}

func (f *fakeRetriever) SearchHelp(_ context.Context, _ []float32, _ int) ([]Citation, error) {
	return f.help, nil
}

func (f *fakeRetriever) SearchHelpLexical(_ context.Context, _ string, _ int) ([]Citation, error) {
	return f.helpLex, nil
}

func TestAskGroundsAndCites(t *testing.T) {
	ret := &fakeRetriever{tenant: []Citation{{SourceType: "record", SourceID: "rec-1", Snippet: "Acme deal"}}}
	llm := &FakeLLM{Reply: "Acme is in negotiation. [1]"}
	emb := &FakeEmbedder{Dim: 768}
	o := NewOrchestrator(emb, ret, llm)

	res, err := o.Ask(context.Background(), AskRequest{Question: "status of Acme?", Scope: "own", CallerUserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" || len(res.Citations) != 1 {
		t.Fatalf("want grounded answer + 1 citation, got %+v", res)
	}
	if !strings.Contains(strings.ToLower(llm.GotSystem), "only") { // system prompt enforces grounding
		t.Fatalf("system prompt must constrain to provided context")
	}
}

func TestAskEmptyRetrievalDoesNotFabricate(t *testing.T) {
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, &fakeRetriever{}, &FakeLLM{Reply: "I don't have that information."})
	res, _ := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"})
	if len(res.Citations) != 0 {
		t.Fatalf("no context -> no citations, got %+v", res.Citations)
	}
}

func TestAskCombinesTenantAndHelpCitations(t *testing.T) {
	ret := &fakeRetriever{
		tenant: []Citation{{SourceType: "record", SourceID: "rec-1", Snippet: "Acme deal"}},
		help:   []Citation{{SourceType: "help", SourceID: "Getting Started", Snippet: "How to create a lead"}},
	}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, &FakeLLM{Reply: "See [1] and [2]."})
	res, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "all", CallerUserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Citations) != 2 {
		t.Fatalf("want 2 citations (1 record + 1 help), got %+v", res.Citations)
	}
}

// TestAskOnlyReturnsCitedReferences guards against showing the caller every
// retrieved chunk as "referenced" when the model only actually used some of
// them — this matters most at low record counts, where top-k retrieval
// returns the tenant's whole record set regardless of true relevance (no
// similarity floor in buildScopedSearch).
func TestAskOnlyReturnsCitedReferences(t *testing.T) {
	ret := &fakeRetriever{tenant: []Citation{
		{SourceType: "record", SourceID: "rec-1", Snippet: "Lead Qualified"},
		{SourceType: "record", SourceID: "rec-2", Snippet: "Lead Unqualified"},
		{SourceType: "record", SourceID: "rec-3", Snippet: "Customer Closed Won"},
	}}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, &FakeLLM{Reply: "The qualified lead is rec-1. [1]"})
	res, err := o.Ask(context.Background(), AskRequest{Question: "which leads are qualified?", Scope: "all", CallerUserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Citations) != 1 || res.Citations[0].SourceID != "rec-1" {
		t.Fatalf("want only rec-1 cited, got %+v", res.Citations)
	}
}

// TestAskNoMarkersMeansNoCitations covers a model reply that never uses [n]
// markers at all (e.g. ignoring the system prompt's instruction) — the
// caller must not see any of the retrieved context misrepresented as "used."
func TestAskNoMarkersMeansNoCitations(t *testing.T) {
	ret := &fakeRetriever{tenant: []Citation{{SourceType: "record", SourceID: "rec-1", Snippet: "Acme deal"}}}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, &FakeLLM{Reply: "I don't have that information."})
	res, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Citations) != 0 {
		t.Fatalf("no [n] markers -> no citations, got %+v", res.Citations)
	}
}

// TestAskGroundsUsingFullContentNotSnippet guards against regressing to
// building the LLM's context out of Citation.Snippet, which is deliberately
// truncated to a 240-char single-line UI preview (see ai/store.go snippet).
// The model must see Content — the fuller, structure-preserving text — or
// any fact past the 240th character of a chunk becomes invisible to it even
// though it was correctly retrieved.
func TestAskGroundsUsingFullContentNotSnippet(t *testing.T) {
	long := strings.Repeat("line about the embedding model\n", 20) // > 240 chars
	ret := &fakeRetriever{help: []Citation{{
		SourceType: "help", SourceID: "Environment variables",
		Snippet: "a short truncated preview only…",
		Content: long,
	}}}
	llm := &FakeLLM{Reply: "See [1]."}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, llm)

	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "all", CallerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if len(llm.GotMessages) != 1 {
		t.Fatalf("want 1 message sent to the LLM, got %d", len(llm.GotMessages))
	}
	if !strings.Contains(llm.GotMessages[0].Content, long) {
		t.Fatalf("LLM context must contain the full Content, not just the truncated Snippet:\n%s", llm.GotMessages[0].Content)
	}
}

func TestAskPropagatesEmbedError(t *testing.T) {
	o := NewOrchestrator(&FakeEmbedder{Err: errBoom}, &fakeRetriever{}, &FakeLLM{Reply: "x"})
	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"}); err == nil {
		t.Fatal("expected embed error to propagate")
	}
}

func TestAskPropagatesRetrievalError(t *testing.T) {
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, &fakeRetriever{err: errBoom}, &FakeLLM{Reply: "x"})
	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"}); err == nil {
		t.Fatal("expected retrieval error to propagate")
	}
}

// TestHasRelevantMatch covers the relevance-floor predicate directly: a
// lexical hit (no distance to judge) always counts, a vector hit needs to
// clear relevanceFloorDistance, and an empty list never counts.
func TestHasRelevantMatch(t *testing.T) {
	tests := []struct {
		name  string
		cites []Citation
		want  bool
	}{
		{"empty list", nil, false},
		{"lexical-only hit (no distance) counts", []Citation{{SourceID: "a"}}, true},
		{"vector hit at floor counts", []Citation{{SourceID: "a", Distance: relevanceFloorDistance, DistanceValid: true}}, true},
		{"vector hit under floor counts", []Citation{{SourceID: "a", Distance: 0.1, DistanceValid: true}}, true},
		{"vector hit over floor alone does not count", []Citation{{SourceID: "a", Distance: 1.5, DistanceValid: true}}, false},
		{"one over floor, one lexical: counts", []Citation{
			{SourceID: "a", Distance: 1.5, DistanceValid: true},
			{SourceID: "b"},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasRelevantMatch(tt.cites); got != tt.want {
				t.Fatalf("hasRelevantMatch(%+v) = %v, want %v", tt.cites, got, tt.want)
			}
		})
	}
}

// TestAskDropsVectorOnlyMatchesBeyondRelevanceFloor is the Ask()-level proof
// behind hasRelevantMatch: a tenant vector arm that only found weak matches
// (all beyond relevanceFloorDistance, no lexical hits either) must ground the
// LLM in nothing rather than handing it noise to reason over — the help arm,
// which found something relevant, still comes through untouched.
func TestAskDropsVectorOnlyMatchesBeyondRelevanceFloor(t *testing.T) {
	ret := &fakeRetriever{
		tenant: []Citation{
			{SourceType: "record", SourceID: "rec-1", Content: "unrelated", Distance: 1.5, DistanceValid: true},
		},
		help: []Citation{
			{SourceType: "help", SourceID: "Getting Started", Content: "how to create a lead", Distance: 0.2, DistanceValid: true},
		},
	}
	llm := &FakeLLM{Reply: "See [1]."}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, llm)

	res, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "all", CallerUserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(llm.GotMessages[0].Content, "unrelated") {
		t.Fatalf("weak vector-only tenant match must not ground the LLM:\n%s", llm.GotMessages[0].Content)
	}
	if !strings.Contains(llm.GotMessages[0].Content, "how to create a lead") {
		t.Fatalf("relevant help match must still ground the LLM:\n%s", llm.GotMessages[0].Content)
	}
	if len(res.Citations) != 1 || res.Citations[0].SourceID != "Getting Started" {
		t.Fatalf("want only the help citation surfaced, got %+v", res.Citations)
	}
}

// TestAskFusesLexicalAndVector proves the lexical arm actually contributes to
// grounding: a hit present ONLY in tenantLex (absent from the vector arm's
// tenant list) must still be able to appear in the grounded context and, once
// the model cites it, in the returned citations. This is the hybrid-retrieval
// behavior this feature adds on top of vector-only search.
func TestAskFusesLexicalAndVector(t *testing.T) {
	ret := &fakeRetriever{
		tenant:    []Citation{{SourceType: "record", SourceID: "rec-vec", Snippet: "semantically similar"}},
		tenantLex: []Citation{{SourceType: "record", SourceID: "rec-lex", Snippet: "INC-2023-Q4-011"}},
	}
	llm := &FakeLLM{Reply: "placeholder"}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, llm)

	if _, err := o.Ask(context.Background(), AskRequest{Question: "INC-2023-Q4-011", Scope: "all", CallerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if len(llm.GotMessages) != 1 || !strings.Contains(llm.GotMessages[0].Content, "INC-2023-Q4-011") {
		t.Fatalf("lexical-only hit must be present in the grounded context, got:\n%s", llm.GotMessages[0].Content)
	}

	// Now confirm it also surfaces in citations once the model cites its marker.
	fused := fuseRRF(tenantRetrievalK, ret.tenant, ret.tenantLex)
	idx := -1
	for i, c := range fused {
		if c.SourceID == "rec-lex" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("rec-lex must appear in the fused list, got %+v", fused)
	}
	llm2 := &FakeLLM{Reply: fmt.Sprintf("See [%d].", idx+1)}
	o2 := NewOrchestrator(&FakeEmbedder{Dim: 768}, ret, llm2)
	res, err := o2.Ask(context.Background(), AskRequest{Question: "INC-2023-Q4-011", Scope: "all", CallerUserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Citations {
		if c.SourceID == "rec-lex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want rec-lex (lexical-only hit) among citations once cited, got %+v", res.Citations)
	}
}

// TestAskRecordsMetricsOnSuccess confirms WithMetrics wires ObserveEmbed once
// and ObserveLLM/ObserveAsk once each with the non-refused, non-timeout
// values for a normal successful ask.
func TestAskRecordsMetricsOnSuccess(t *testing.T) {
	m := &fakeMetrics{}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, &fakeRetriever{}, &FakeLLM{Reply: "Acme is in negotiation. [1]"}).WithMetrics(m)
	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if m.embedCalls != 1 {
		t.Fatalf("embedCalls = %d, want 1", m.embedCalls)
	}
	if len(m.llmSeconds) != 1 || m.llmTimeout[0] {
		t.Fatalf("llm observation = %v/%v, want one non-timeout call", m.llmSeconds, m.llmTimeout)
	}
	if len(m.askRefused) != 1 || m.askRefused[0] {
		t.Fatalf("askRefused = %v, want one non-refused call", m.askRefused)
	}
}

// TestAskRecordsRefusalMetric confirms ObserveAsk(true) when the model's
// answer contains the exact refusal phrase the system prompt instructs it to
// use — the refusal-rate signal the RAG architecture review calls for.
func TestAskRecordsRefusalMetric(t *testing.T) {
	m := &fakeMetrics{}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, &fakeRetriever{}, &FakeLLM{Reply: refusalPhrase}).WithMetrics(m)
	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if len(m.askRefused) != 1 || !m.askRefused[0] {
		t.Fatalf("askRefused = %v, want one refused call", m.askRefused)
	}
}

// TestAskRecordsLLMTimeoutMetric confirms ObserveLLM's timedOut flag is set
// when the LLM client's own deadline elapses (context.DeadlineExceeded),
// distinguishing that from any other chat-completion failure.
func TestAskRecordsLLMTimeoutMetric(t *testing.T) {
	m := &fakeMetrics{}
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, &fakeRetriever{}, &FakeLLM{Err: context.DeadlineExceeded}).WithMetrics(m)
	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"}); err == nil {
		t.Fatal("expected the LLM error to propagate")
	}
	if len(m.llmTimeout) != 1 || !m.llmTimeout[0] {
		t.Fatalf("llmTimeout = %v, want one timeout call", m.llmTimeout)
	}
	// A timed-out LLM call never produces an answer, so no ask-level
	// refused/answered observation should be recorded for it.
	if len(m.askRefused) != 0 {
		t.Fatalf("askRefused = %v, want none recorded on LLM error", m.askRefused)
	}
}

// TestAskWithoutMetricsDoesNotPanic confirms an Orchestrator built without
// WithMetrics (the noopMetrics default) works exactly as before this feature.
func TestAskWithoutMetricsDoesNotPanic(t *testing.T) {
	o := NewOrchestrator(&FakeEmbedder{Dim: 768}, &fakeRetriever{}, &FakeLLM{Reply: "ok"})
	if _, err := o.Ask(context.Background(), AskRequest{Question: "x", Scope: "own", CallerUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
}

package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var errBoom = errors.New("boom")

type fakeRetriever struct {
	tenant []Citation
	help   []Citation
	err    error
}

func (f *fakeRetriever) SearchScoped(_ context.Context, _ []float32, _, _ string, _ []string, _ int) ([]Citation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tenant, nil
}

func (f *fakeRetriever) SearchHelp(_ context.Context, _ []float32, _ int) ([]Citation, error) {
	return f.help, nil
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

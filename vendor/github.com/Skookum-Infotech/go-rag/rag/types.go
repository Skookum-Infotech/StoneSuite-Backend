// Package ai holds the provider-agnostic AI/RAG primitives: the Embedder and
// LLMClient interfaces, provider implementations, and pure record-rendering
// helpers. It deliberately depends on nothing app-specific (like the query
// package) so any store can use it without an import cycle.
package rag

import "context"

// Message is one turn in a chat exchange. Role is "user" or "assistant".
type Message struct {
	Role    string
	Content string
}

// Citation points back at a source chunk used to ground an answer.
// SourceType is "record" (tenant data) or "help" (control-plane app docs).
type Citation struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id,omitempty"`
	// Snippet is a short, single-line preview for UI display only.
	Snippet string `json:"snippet"`
	// Content is the fuller chunk text the LLM actually reasons over — not
	// serialized to the API response (json:"-"). Kept separate from Snippet
	// so truncating the UI preview to one line can never also truncate what
	// the model is grounded in.
	Content string `json:"-"`
	// Distance is the pgvector cosine distance (0 = identical, larger = less
	// similar) from the vector-arm search that produced this citation — the
	// relevance-floor signal (see ai/orchestrator.go hasRelevantMatch). Only
	// meaningful when DistanceValid is true; a lexical-only match (full-text,
	// no vector search involved) leaves both at their zero value. Internal,
	// never serialized (json:"-").
	Distance      float64 `json:"-"`
	DistanceValid bool    `json:"-"`
}

// Embedder turns text into vectors. Implementations must return one vector per
// input text, in the same order, each of length config.AppConfig.AIEmbedDim.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Fingerprinter is an optional companion to Embedder: it names the vector space
// an implementation produces, so callers can tell whether an already-stored
// vector is still comparable to a freshly-embedded query.
//
// Optional rather than part of Embedder so test doubles and simple
// implementations stay trivial; callers fall back to an empty fingerprint,
// which is safe (it just means "cannot distinguish vector spaces") as long as
// nothing depends on distinguishing them. See VectorHash.
type Fingerprinter interface {
	Fingerprint() string
}

// LLMClient produces a completion given a system prompt and a message history.
type LLMClient interface {
	Chat(ctx context.Context, system string, messages []Message) (string, error)
}

// StructuredLLMClient is an optional companion to LLMClient (the same
// point-of-use pattern as Fingerprinter and Reranker): an LLMClient that can
// additionally constrain its output to a JSON schema server-side — e.g.
// Ollama's "format" chat parameter — rather than merely being asked nicely to
// return JSON in prose.
//
// This matters for schema-constrained routing (see route.Extract): a small
// model asked in plain language to "reply with JSON" drifts under load —
// prose before the JSON, trailing commentary, occasional plain refusal text.
// Server-side schema constraints make the decoder itself incapable of
// producing anything else, which is what makes routing reliable on a 3B
// model. schema is raw JSON Schema bytes so this package stays free of a
// schema-building dependency; callers construct it once and reuse it.
//
// Optional rather than folded into LLMClient: a client without this
// capability simply cannot be used for routing — callers type-assert for it
// and treat its absence as "routing unavailable", not an error.
type StructuredLLMClient interface {
	ChatJSON(ctx context.Context, system string, messages []Message, schema []byte) (string, error)
}

// Reranker re-scores retrieval candidates against the original question and
// returns up to n of them, most relevant first. A cross-encoder reranker sees
// the question and each candidate together, so it can catch relevance a
// bi-encoder embedding (which scores question and candidate independently)
// misses — at the cost of one model call per batch of candidates instead of
// per corpus, which is why it runs over a small widened candidate set rather
// than the whole corpus.
//
// Optional: an Orchestrator without one (nil) uses RRF-fused order as-is.
// Reranking is strictly an accuracy upgrade, never required for correctness —
// a reranker implementation returning an error must not be treated as fatal
// by callers; falling back to RRF order is the correct degrade.
type Reranker interface {
	Rerank(ctx context.Context, question string, candidates []Citation, n int) ([]Citation, error)
}

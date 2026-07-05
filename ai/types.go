// Package ai holds the provider-agnostic AI/RAG primitives: the Embedder and
// LLMClient interfaces, provider implementations, and pure record-rendering
// helpers. It deliberately depends on nothing app-specific (like the query
// package) so any store can use it without an import cycle.
package ai

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
}

// Embedder turns text into vectors. Implementations must return one vector per
// input text, in the same order, each of length config.AppConfig.AIEmbedDim.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// LLMClient produces a completion given a system prompt and a message history.
type LLMClient interface {
	Chat(ctx context.Context, system string, messages []Message) (string, error)
}

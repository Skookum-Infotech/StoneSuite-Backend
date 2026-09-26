package rag

import (
	"context"
	"fmt"
)

// warmUpMaxTokens is the num_predict WarmUp asks for when llm supports
// maxTokensSetter. Warm-up exists only to pay Ollama's model-LOAD latency
// ahead of the first real request — it has no need to pay for a full
// generation on top of that, and on a CPU-bound box generation time scales
// directly with output length.
const warmUpMaxTokens = 1

// maxTokensSetter is an optional companion to LLMClient (same point-of-use
// pattern as StructuredLLMClient/StreamingLLMClient): an LLMClient that can
// return a copy of itself bounded to a smaller per-call token budget — e.g.
// provider/ollama.LLMClient.WithMaxTokens. WarmUp uses it, when available, so
// its chat call generates at most warmUpMaxTokens tokens instead of paying for
// a full answer it throws away.
//
// Optional rather than folded into LLMClient: a client without this
// capability simply can't be asked for fewer tokens, and WarmUp degrades to
// its full default budget rather than treating the absence as an error.
type maxTokensSetter interface {
	WithMaxTokens(n int) LLMClient
}

// WarmUp fires one trivial embedding + chat completion so the first real
// /ai/ask request isn't the one paying Ollama's model-load latency: Ollama
// loads a model lazily on its first inference even though the box's Fly
// Machine has already booted (see services.OllamaLifecycle). The chat call
// generates at most warmUpMaxTokens tokens when llm implements
// maxTokensSetter — model load happens on the first inference regardless of
// how many tokens are then generated, so there is nothing to gain from
// letting warm-up run a full completion. Errors are wrapped, not swallowed —
// callers decide whether a failed warmup is worth logging. Non-fatal by
// design either way: a cold real request just pays the same model-load
// latency itself, same as if this didn't run at all.
func WarmUp(ctx context.Context, emb Embedder, llm LLMClient) error {
	if _, err := emb.Embed(ctx, []string{"warmup"}); err != nil {
		return fmt.Errorf("warmup embed: %w", err)
	}
	chatLLM := llm
	if setter, ok := llm.(maxTokensSetter); ok {
		chatLLM = setter.WithMaxTokens(warmUpMaxTokens)
	}
	if _, err := chatLLM.Chat(ctx, "", []Message{{Role: "user", Content: "warmup"}}); err != nil {
		return fmt.Errorf("warmup chat: %w", err)
	}
	return nil
}

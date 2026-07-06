package ai

import (
	"context"
	"fmt"
)

// WarmUp fires one trivial embedding + chat completion so the first real
// /ai/ask request isn't the one paying Ollama's model-load latency: Ollama
// loads a model lazily on its first inference even though the box's Fly
// Machine has already booted (see services.OllamaLifecycle). Errors are
// wrapped, not swallowed — callers decide whether a failed warmup is worth
// logging. Non-fatal by design either way: a cold real request just pays the
// same model-load latency itself, same as if this didn't run at all.
func WarmUp(ctx context.Context, emb Embedder, llm LLMClient) error {
	if _, err := emb.Embed(ctx, []string{"warmup"}); err != nil {
		return fmt.Errorf("warmup embed: %w", err)
	}
	if _, err := llm.Chat(ctx, "", []Message{{Role: "user", Content: "warmup"}}); err != nil {
		return fmt.Errorf("warmup chat: %w", err)
	}
	return nil
}

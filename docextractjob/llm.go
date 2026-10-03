package docextractjob

import (
	"context"
	"fmt"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/metrics"
)

// userRole is the chat role of the document prompt.
const userRole = "user"

// Waker starts the self-hosted Ollama machines on demand (services.OllamaWaker).
type Waker interface {
	EnsureRunning(ctx context.Context) error
}

// llmAdapter adapts the app's chat client to docextract.LLM. It wakes Ollama
// lazily -- only when the fallback actually calls it -- and counts the calls
// and tokens of one job.
type llmAdapter struct {
	client ragcore.LLMClient
	waker  Waker // may be nil

	calls            int
	promptTokens     int
	completionTokens int
}

// Generate wakes Ollama if needed, then runs one system+user chat turn.
func (a *llmAdapter) Generate(ctx context.Context, system, user string) (string, error) {
	a.calls++
	metrics.ObserveDocExtractLLMCall()
	if a.waker != nil {
		if err := a.waker.EnsureRunning(ctx); err != nil {
			return "", fmt.Errorf("wake llm: %w", err)
		}
	}
	uctx, usage := ragcore.WithUsage(ctx)
	out, err := a.client.Chat(uctx, system, []ragcore.Message{{Role: userRole, Content: user}})
	a.promptTokens += usage.PromptTokens
	a.completionTokens += usage.CompletionTokens
	if err != nil {
		return "", fmt.Errorf("llm chat: %w", err)
	}
	return out, nil
}

package usage

import (
	"context"
	"errors"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

// errNotSupported is returned when the wrapped client lacks the optional
// capability a caller asked for. It never happens with the Ollama client,
// which implements all of them.
var errNotSupported = errors.New("usage: wrapped llm client does not support this call")

// entryRecorder queues one metered call (*Recorder in production).
type entryRecorder interface {
	Record(e TokenEntry)
}

// tokenBounded is a client that can cap one call's output tokens
// (provider/ollama.LLMClient.WithMaxTokens).
type tokenBounded interface {
	WithMaxTokens(n int) ragcore.LLMClient
}

// MeteredLLM wraps the Ollama chat client so every call's token counts are
// attributed to the tenant on the context (see TenantFrom / WithSource) and
// written to the daily usage rollup. It is the single metering chokepoint:
// every chat path (assistant, routed counts, report interpret, document
// extraction, import) shares the one client built in newChatClient.
//
// It implements the optional rag.StructuredLLMClient and
// rag.StreamingLLMClient companions and WithMaxTokens, delegating to the
// wrapped client; it is meant to wrap a client that has them all.
//
// TODO(usage): embedding calls are not metered — go-rag's Ollama embed
// response does not decode prompt_eval_count yet.
type MeteredLLM struct {
	inner ragcore.LLMClient
	model string
	rec   entryRecorder
	now   func() time.Time
}

// NewMeteredLLM wraps inner; model names the rollup row (the configured chat model).
func NewMeteredLLM(inner ragcore.LLMClient, model string, rec entryRecorder) *MeteredLLM {
	return &MeteredLLM{inner: inner, model: model, rec: rec, now: time.Now}
}

// Chat meters one non-streaming completion.
func (m *MeteredLLM) Chat(ctx context.Context, system string, messages []ragcore.Message) (string, error) {
	var out string
	err := m.meter(ctx, func(c context.Context) error {
		var err error
		out, err = m.inner.Chat(c, system, messages)
		return err
	})
	return out, err
}

// ChatJSON meters one schema-constrained completion.
func (m *MeteredLLM) ChatJSON(ctx context.Context, system string, messages []ragcore.Message, schema []byte) (string, error) {
	structured, ok := m.inner.(ragcore.StructuredLLMClient)
	if !ok {
		return "", errNotSupported
	}
	var out string
	err := m.meter(ctx, func(c context.Context) error {
		var err error
		out, err = structured.ChatJSON(c, system, messages, schema)
		return err
	})
	return out, err
}

// ChatStream meters one streamed completion (Ollama reports usage on the
// final chunk, so it is recorded once the stream ends).
func (m *MeteredLLM) ChatStream(ctx context.Context, system string, messages []ragcore.Message, onToken func(string) error) (string, error) {
	streamer, ok := m.inner.(ragcore.StreamingLLMClient)
	if !ok {
		return "", errNotSupported
	}
	var out string
	err := m.meter(ctx, func(c context.Context) error {
		var err error
		out, err = streamer.ChatStream(c, system, messages, onToken)
		return err
	})
	return out, err
}

// WithMaxTokens returns a metered copy bounded to n output tokens, so the
// per-intent and report-interpret budgets stay metered too.
func (m *MeteredLLM) WithMaxTokens(n int) ragcore.LLMClient {
	bounded, ok := m.inner.(tokenBounded)
	if !ok {
		return m
	}
	cp := *m
	cp.inner = bounded.WithMaxTokens(n)
	return &cp
}

// meter runs call under its own usage slot, passes the counts back up to any
// slot the caller placed on ctx (rag.ReportUsage overwrites the innermost
// slot, so without this the orchestrator's own usage metrics would read
// zero), and records them for the tenant. Tokens are recorded even when the
// call errored after the model had already done the work.
func (m *MeteredLLM) meter(ctx context.Context, call func(context.Context) error) error {
	inner, slot := ragcore.WithUsage(ctx)
	err := call(inner)
	u := *slot
	ragcore.ReportUsage(ctx, u)
	m.record(ctx, u)
	return err
}

func (m *MeteredLLM) record(ctx context.Context, u ragcore.Usage) {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return
	}
	tenantID := TenantFrom(ctx)
	source := SourceFrom(ctx)
	if tenantID == "" || source == SourceWarmup || m.rec == nil {
		return
	}
	m.rec.Record(TokenEntry{
		TenantID:         tenantID,
		Day:              midnightUTC(m.now()),
		Source:           source,
		Model:            m.model,
		PromptTokens:     int64(u.PromptTokens),
		CompletionTokens: int64(u.CompletionTokens),
	})
}

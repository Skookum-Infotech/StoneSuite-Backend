package rag

import (
	"context"
	"errors"
)

// ErrUnavailable marks a model-backend failure that is expected to clear on
// its own — connection refused, a cold box still booting, a 502/503/504 from
// the proxy in front of it. Callers map it to "try again shortly" rather than
// a generic failure. Providers wrap it with %w.
var ErrUnavailable = errors.New("model backend unavailable")

// ErrModelNotFound marks a backend that is up but does not have the requested
// model — an operator problem (model not pulled), not a transient one.
var ErrModelNotFound = errors.New("model not found on backend")

// Usage is what one LLM call reports about its own generation: token counts
// for metrics, and whether the answer was cut off by the output-token cap.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	// Truncated is true when generation stopped at the token cap rather than
	// finishing naturally — the answer may end mid-sentence or miss citations.
	Truncated bool
}

type usageKey struct{}

// WithUsage returns a child context carrying a fresh Usage slot, and the slot
// itself. A provider that can report usage fills it via ReportUsage; one that
// can't leaves it zero. Carried on the context rather than returned so the
// LLMClient/StreamingLLMClient interfaces stay unchanged for every existing
// implementation and fake.
func WithUsage(ctx context.Context) (context.Context, *Usage) {
	u := &Usage{}
	return context.WithValue(ctx, usageKey{}, u), u
}

// ReportUsage records u into the slot WithUsage placed on ctx, if any.
func ReportUsage(ctx context.Context, u Usage) {
	if slot, ok := ctx.Value(usageKey{}).(*Usage); ok && slot != nil {
		*slot = u
	}
}

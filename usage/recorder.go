package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultRecorderBuffer is how many metered calls may wait to be written
// before Record starts dropping (and logging) new ones.
const DefaultRecorderBuffer = 1024

// recordWriteTimeout bounds one rollup write so a slow control plane cannot
// wedge the recorder goroutine.
const recordWriteTimeout = 5 * time.Second

// tokenWriter persists one metered call (*Store in production).
type tokenWriter interface {
	RecordTokens(ctx context.Context, e TokenEntry) error
}

// Recorder writes metered calls asynchronously, so metering never adds
// latency to — and can never fail — the LLM call it measures. Start it once,
// Close it on shutdown to flush what is queued.
type Recorder struct {
	writer tokenWriter
	ch     chan TokenEntry
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

// NewRecorder builds a Recorder with room for buffer queued entries.
func NewRecorder(writer tokenWriter, buffer int) *Recorder {
	return &Recorder{writer: writer, ch: make(chan TokenEntry, buffer), done: make(chan struct{})}
}

// Start runs the single writer goroutine. It exits once Close drains the queue.
func (r *Recorder) Start() {
	go func() {
		defer close(r.done)
		for e := range r.ch {
			ctx, cancel := context.WithTimeout(context.Background(), recordWriteTimeout)
			if err := r.writer.RecordTokens(ctx, e); err != nil {
				slog.Error("usage: failed to record llm tokens", "tenant_id", e.TenantID,
					"source", e.Source, "prompt_tokens", e.PromptTokens, "completion_tokens", e.CompletionTokens, "err", err)
			}
			cancel()
		}
	}()
}

// Record queues e without blocking. A full queue or a closed recorder drops
// the entry with a warning rather than slowing the caller.
func (r *Recorder) Record(e TokenEntry) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		slog.Warn("usage: recorder closed, dropping llm token entry", "tenant_id", e.TenantID, "source", e.Source)
		return
	}
	select {
	case r.ch <- e:
	default:
		slog.Warn("usage: recorder queue full, dropping llm token entry", "tenant_id", e.TenantID,
			"source", e.Source, "prompt_tokens", e.PromptTokens, "completion_tokens", e.CompletionTokens)
	}
}

// Close stops accepting entries and waits (up to ctx) for the queue to drain.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

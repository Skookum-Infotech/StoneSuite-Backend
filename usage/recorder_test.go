package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWriter struct {
	mu      sync.Mutex
	entries []TokenEntry
	err     error
	block   chan struct{}
}

func (w *fakeWriter) RecordTokens(_ context.Context, e TokenEntry) error {
	if w.block != nil {
		<-w.block
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, e)
	return w.err
}

func TestRecorder_FlushesOnClose(t *testing.T) {
	w := &fakeWriter{}
	r := NewRecorder(w, DefaultRecorderBuffer)
	r.Start()
	r.Record(TokenEntry{TenantID: "a"})
	r.Record(TokenEntry{TenantID: "b"})
	require.NoError(t, r.Close(context.Background()))
	assert.Len(t, w.entries, 2)

	r.Record(TokenEntry{TenantID: "late"}) // after close: dropped, no panic
	assert.Len(t, w.entries, 2)
	require.NoError(t, r.Close(context.Background()), "close is idempotent")
}

func TestRecorder_DropsWhenFull(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	r := NewRecorder(w, 1)
	r.Start()
	r.Record(TokenEntry{TenantID: "in-flight"})
	// Wait until the writer goroutine has taken the first entry off the queue.
	require.Eventually(t, func() bool { return len(r.ch) == 0 }, time.Second, time.Millisecond)
	r.Record(TokenEntry{TenantID: "queued"})
	r.Record(TokenEntry{TenantID: "dropped"})
	close(w.block)
	require.NoError(t, r.Close(context.Background()))
	ids := []string{}
	for _, e := range w.entries {
		ids = append(ids, e.TenantID)
	}
	assert.Equal(t, []string{"in-flight", "queued"}, ids)
}

func TestRecorder_WriteErrorDoesNotStopRecorder(t *testing.T) {
	w := &fakeWriter{err: errors.New("cp down")}
	r := NewRecorder(w, DefaultRecorderBuffer)
	r.Start()
	r.Record(TokenEntry{TenantID: "a"})
	r.Record(TokenEntry{TenantID: "b"})
	require.NoError(t, r.Close(context.Background()))
	assert.Len(t, w.entries, 2)
}

func TestRecorder_CloseHonoursContext(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	r := NewRecorder(w, DefaultRecorderBuffer)
	r.Start()
	r.Record(TokenEntry{TenantID: "stuck"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, r.Close(ctx), context.DeadlineExceeded)
	close(w.block)
}

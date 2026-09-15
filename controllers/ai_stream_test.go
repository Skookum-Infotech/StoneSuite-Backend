package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

func TestAIOpsAskStream_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/ask/stream", nil)
	w := httptest.NewRecorder()

	h.AskStream(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "event-stream") {
		t.Fatalf("an auth failure must be an ordinary JSON error, not SSE headers: Content-Type=%q", ct)
	}
}

// TestAIOpsAcquireStream_BoundsConcurrency proves the semaphore actually
// bounds concurrent streams: maxConcurrentStreams acquires succeed, the next
// one is refused, and releasing frees a slot back up.
func TestAIOpsAcquireStream_BoundsConcurrency(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)

	for i := 0; i < maxConcurrentStreams; i++ {
		if !h.acquireStream() {
			t.Fatalf("acquire %d should have succeeded (limit is %d)", i, maxConcurrentStreams)
		}
	}
	if h.acquireStream() {
		t.Fatal("acquire beyond maxConcurrentStreams should fail")
	}

	h.releaseStream()
	if !h.acquireStream() {
		t.Fatal("acquire should succeed again after a release freed a slot")
	}
}

func TestWriteSSE_FormatsAsEventDataFrame(t *testing.T) {
	var buf strings.Builder
	if err := writeSSE(&buf, "token", "hello"); err != nil {
		t.Fatal(err)
	}
	want := "event: token\ndata: \"hello\"\n\n"
	if buf.String() != want {
		t.Fatalf("writeSSE output = %q, want %q", buf.String(), want)
	}
}

func TestWriteSSE_ComplexDataStaysOneLine(t *testing.T) {
	var buf strings.Builder
	if err := writeSSE(&buf, "done", map[string]any{"answer": "line one", "citations": []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	// Exactly one data: line — a multi-line payload would break SSE framing,
	// since everything after the first newline would be read as a new field.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	dataLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "data: ") {
			dataLines++
		}
	}
	if dataLines != 1 {
		t.Fatalf("want exactly 1 data: line, got %d in %q", dataLines, buf.String())
	}
}

func TestChannelSink_ForwardsEventsInOrder(t *testing.T) {
	ch := make(chan sseEvent, 4)
	sink := &channelSink{ch: ch, ctx: context.Background()}

	if err := sink.OnRetrieved([]ragcore.Citation{{SourceID: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := sink.OnToken("hello"); err != nil {
		t.Fatal(err)
	}
	close(ch)

	var got []string
	for ev := range ch {
		got = append(got, ev.event)
	}
	if len(got) != 2 || got[0] != "sources" || got[1] != "token" {
		t.Fatalf("events = %v, want [sources token]", got)
	}
}

// TestChannelSink_AbortsOnContextDone proves a sink send never blocks
// forever once the handler has given up — required so AskStream's
// background dispatch goroutine (and, beneath it, ChatStream) actually
// unwinds instead of leaking when the client disconnects or the absolute
// stream timeout fires.
func TestChannelSink_AbortsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// An unbuffered, never-read channel: without ctx-awareness this send
	// would block forever.
	ch := make(chan sseEvent)
	sink := &channelSink{ch: ch, ctx: ctx}

	if err := sink.OnToken("x"); err == nil {
		t.Fatal("expected OnToken to return the context's error once ctx is done")
	}
}

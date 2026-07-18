package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Compile-time proof OllamaEmbedder is an Embedder.
var _ Embedder = (*OllamaEmbedder)(nil)

func TestOllamaEmbedAppliesDocPrefix(t *testing.T) {
	var gotPrompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/embeddings") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(body, &req)
		gotPrompts = append(gotPrompts, req.Prompt)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.1, 0.2, 0.3}})
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text")
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 {
		t.Fatalf("got %d vectors of dim %d, want 2 of dim 3", len(vecs), len(vecs[0]))
	}
	for _, p := range gotPrompts {
		if !strings.HasPrefix(p, "search_document: ") {
			t.Fatalf("doc embedder must prefix search_document: , got %q", p)
		}
	}
}

func TestOllamaQueryEmbedderUsesQueryPrefix(t *testing.T) {
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(body, &req)
		gotPrompt = req.Prompt
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.4, 0.5, 0.6}})
	}))
	defer srv.Close()

	e := NewOllamaQueryEmbedder(srv.URL, "nomic-embed-text")
	if _, err := e.Embed(context.Background(), []string{"who is acme?"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotPrompt, "search_query: ") {
		t.Fatalf("query embedder must prefix search_query: , got %q", gotPrompt)
	}
}

func TestOllamaEmbedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"model not loaded"}`))
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text")
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error should mention status 503: %v", err)
	}
}

// TestOllamaEmbedRetriesTransportFailure covers the scale-to-zero
// autostart/autostop race: the embedder box can reset a connection while it's
// mid start or stop. A transport-level failure (not an HTTP error response)
// must be retried, not surfaced immediately.
func TestOllamaEmbedRetriesTransportFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// Simulate a reset connection: hijack and close without a response.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.1, 0.2}})
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text")
	e.retryDelay = time.Millisecond // keep the test fast

	vecs, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("expected retry to recover, got: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 {
		t.Fatalf("got %v, want one 2-dim vector", vecs)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one failure + one successful retry)", calls.Load())
	}
}

// TestOllamaEmbedRecoversFromExtendedColdStart proves the retry budget covers
// a slower Ollama Machine wake-up than a single failure+retry: several
// consecutive transport failures before the machine becomes reachable must
// still resolve successfully once it does.
func TestOllamaEmbedRecoversFromExtendedColdStart(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= transportRetries {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.1, 0.2}})
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text")
	e.retryDelay = time.Millisecond

	vecs, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("expected the retry budget to recover from an extended cold start, got: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 {
		t.Fatalf("got %v, want one 2-dim vector", vecs)
	}
	if calls.Load() != transportRetries+1 {
		t.Fatalf("calls = %d, want %d (failed on every attempt but the last)", calls.Load(), transportRetries+1)
	}
}

// TestOllamaEmbedGivesUpAfterTransportRetriesExhausted proves the retry loop
// is bounded — a sustained outage must still surface an error, not retry
// forever.
func TestOllamaEmbedGivesUpAfterTransportRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text")
	e.retryDelay = time.Millisecond

	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if calls.Load() != transportRetries+1 {
		t.Fatalf("calls = %d, want %d (initial attempt + %d retries)", calls.Load(), transportRetries+1, transportRetries)
	}
}

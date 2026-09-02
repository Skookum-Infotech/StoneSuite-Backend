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

// embedServer stands in for Ollama's POST /api/embed. It records every batch of
// inputs it was sent and answers with dim-wide vectors, one per input — the
// shape the real endpoint guarantees and that embedBatch validates.
func embedServer(t *testing.T, dim int, batches *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/embed") {
			t.Errorf("unexpected path %s, want /api/embed", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req ollamaEmbedReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if batches != nil {
			*batches = append(*batches, req.Input)
		}
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = make([]float32, dim)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
}

func TestOllamaEmbedAppliesModelPrefixes(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		wantDoc, wantQu string
	}{
		{"nomic uses symmetric search_ prefixes", "nomic-embed-text", "search_document: ", "search_query: "},
		{"arctic embeds documents bare, instructs the query", "snowflake-arctic-embed:m", "", "Represent this sentence for searching relevant passages: "},
		{"tag is stripped before lookup", "nomic-embed-text:v1.5", "search_document: ", "search_query: "},
		{"unknown model falls back to no prefixes", "some-future-model", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var batches [][]string
			srv := embedServer(t, 3, &batches)
			defer srv.Close()

			if _, err := NewOllamaDocEmbedder(srv.URL, tc.model, 0).Embed(context.Background(), []string{"hello"}); err != nil {
				t.Fatal(err)
			}
			if got := batches[0][0]; got != tc.wantDoc+"hello" {
				t.Errorf("doc input = %q, want %q", got, tc.wantDoc+"hello")
			}

			batches = nil
			if _, err := NewOllamaQueryEmbedder(srv.URL, tc.model, 0).Embed(context.Background(), []string{"who?"}); err != nil {
				t.Fatal(err)
			}
			if got := batches[0][0]; got != tc.wantQu+"who?" {
				t.Errorf("query input = %q, want %q", got, tc.wantQu+"who?")
			}
		})
	}
}

// TestOllamaEmbedPrefixesStayConsistent guards the ADR-001 invariant that
// matters most: doc and query vectors are only comparable when both sides came
// from the same model's registered pair. Looking them up together by model name
// is what makes that structural rather than a thing to remember.
func TestOllamaEmbedPrefixesStayConsistent(t *testing.T) {
	for model := range modelPrefixes {
		doc, query := prefixesFor(model), prefixesFor(model)
		if doc.doc != query.doc || doc.query != query.query {
			t.Errorf("model %s resolved inconsistently", model)
		}
	}
	if p := prefixesFor("definitely-not-registered"); p.doc != "" || p.query != "" {
		t.Errorf("unknown model must fall back to empty prefixes, got %+v", p)
	}
}

// TestOllamaEmbedBatches proves texts go out in batches rather than one request
// per text — the difference between ~10 and ~1000 round-trips on a full reindex.
func TestOllamaEmbedBatches(t *testing.T) {
	var batches [][]string
	srv := embedServer(t, 2, &batches)
	defer srv.Close()

	texts := make([]string, maxEmbedBatch+5)
	for i := range texts {
		texts[i] = "text"
	}

	vecs, err := NewOllamaDocEmbedder(srv.URL, "bge-m3", 0).Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(texts))
	}
	if len(batches) != 2 {
		t.Fatalf("got %d requests, want 2 (a full batch plus the remainder)", len(batches))
	}
	if len(batches[0]) != maxEmbedBatch || len(batches[1]) != 5 {
		t.Fatalf("batch sizes = %d, %d; want %d, 5", len(batches[0]), len(batches[1]), maxEmbedBatch)
	}
}

func TestOllamaEmbedPreservesOrderAcrossBatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req ollamaEmbedReq
		_ = json.Unmarshal(body, &req)
		// Encode each input's first byte into its vector so order is checkable.
		vecs := make([][]float32, len(req.Input))
		for i, in := range req.Input {
			vecs[i] = []float32{float32(in[0])}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	defer srv.Close()

	texts := make([]string, maxEmbedBatch+3)
	for i := range texts {
		texts[i] = string(rune('a'+i%26)) + "x"
	}

	vecs, err := NewOllamaDocEmbedder(srv.URL, "bge-m3", 0).Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range texts {
		if vecs[i][0] != float32(want[0]) {
			t.Fatalf("vector %d is out of order: got %v, want first byte of %q", i, vecs[i][0], want)
		}
	}
}

// TestOllamaEmbedValidatesDimension covers the AI_EMBED_DIM contract. Without
// this the mismatch only surfaces as an opaque pgvector insert failure, with no
// mention of which model produced the wrong width.
func TestOllamaEmbedValidatesDimension(t *testing.T) {
	srv := embedServer(t, 512, nil)
	defer srv.Close()

	_, err := NewOllamaDocEmbedder(srv.URL, "bge-m3", 768).Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected an error when the model returns the wrong width")
	}
	for _, want := range []string{"512", "768", "bge-m3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q for diagnosis: %v", want, err)
		}
	}

	if _, err := NewOllamaDocEmbedder(srv.URL, "bge-m3", 0).Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("dim 0 must disable the check, got: %v", err)
	}
}

func TestOllamaEmbedRejectsShortResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Two inputs requested, one embedding returned — silently misaligning
		// vectors with their source records if not caught here.
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.1}}})
	}))
	defer srv.Close()

	_, err := NewOllamaDocEmbedder(srv.URL, "bge-m3", 0).Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected an error when the model returns fewer embeddings than inputs")
	}
}

func TestOllamaEmbedRejectsEmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{}}})
	}))
	defer srv.Close()

	if _, err := NewOllamaDocEmbedder(srv.URL, "bge-m3", 0).Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected an error on an empty embedding")
	}
}

func TestOllamaEmbedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"model not loaded"}`))
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text", 0)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.1, 0.2}}})
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text", 0)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.1, 0.2}}})
	}))
	defer srv.Close()

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text", 0)
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

	e := NewOllamaDocEmbedder(srv.URL, "nomic-embed-text", 0)
	e.retryDelay = time.Millisecond

	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if calls.Load() != transportRetries+1 {
		t.Fatalf("calls = %d, want %d (initial attempt + %d retries)", calls.Load(), transportRetries+1, transportRetries)
	}
}

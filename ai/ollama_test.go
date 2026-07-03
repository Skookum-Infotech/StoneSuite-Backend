package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		json.Unmarshal(body, &req)
		gotPrompts = append(gotPrompts, req.Prompt)
		json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.1, 0.2, 0.3}})
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
		json.Unmarshal(body, &req)
		gotPrompt = req.Prompt
		json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.4, 0.5, 0.6}})
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
		w.Write([]byte(`{"error":"model not loaded"}`))
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

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Compile-time proof GeminiClient is an LLMClient (and NOT an Embedder).
var _ LLMClient = (*GeminiClient)(nil)

func TestGeminiChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected chat path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{
					"parts": []map[string]any{{"text": "grounded answer"}},
				}},
			},
		})
	}))
	defer srv.Close()

	c := NewGeminiClient("k", "gemini-1.5-flash")
	c.baseURL = srv.URL

	out, err := c.Chat(context.Background(), "system prompt", []Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "grounded answer" {
		t.Fatalf("Chat = %q, want grounded answer", out)
	}
}

func TestGeminiChatAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
	}))
	defer srv.Close()

	c := NewGeminiClient("k", "gemini-1.5-flash")
	c.baseURL = srv.URL

	_, err := c.Chat(context.Background(), "s", []Message{{Role: "user", Content: "q"}})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error should mention status 429: %v", err)
	}
}

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Compile-time proof GroqClient is an LLMClient (and NOT an Embedder).
var _ LLMClient = (*GroqClient)(nil)

func TestGroqChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/openai/v1/chat/completions") {
			t.Errorf("unexpected chat path %s", r.URL.Path)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 0 || body.Messages[0].Role != "system" {
			t.Errorf("expected first message to be role=system, got %+v", body.Messages)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "grounded answer"}},
			},
		})
	}))
	defer srv.Close()

	c := NewGroqClient("k", "llama-3.1-8b-instant")
	c.baseURL = srv.URL

	out, err := c.Chat(context.Background(), "system prompt", []Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "grounded answer" {
		t.Fatalf("Chat = %q, want grounded answer", out)
	}
}

func TestGroqChatAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	c := NewGroqClient("k", "llama-3.1-8b-instant")
	c.baseURL = srv.URL

	_, err := c.Chat(context.Background(), "s", []Message{{Role: "user", Content: "q"}})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error should mention status 429: %v", err)
	}
}

func TestGroqChatMapsAssistantRole(t *testing.T) {
	var gotRoles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			gotRoles = append(gotRoles, m.Role)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	c := NewGroqClient("k", "m")
	c.baseURL = srv.URL
	_, err := c.Chat(context.Background(), "sys", []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system", "user", "assistant"}
	if len(gotRoles) != len(want) {
		t.Fatalf("roles = %v, want %v", gotRoles, want)
	}
	for i, r := range want {
		if gotRoles[i] != r {
			t.Fatalf("roles[%d] = %q, want %q (full: %v)", i, gotRoles[i], r, gotRoles)
		}
	}
}

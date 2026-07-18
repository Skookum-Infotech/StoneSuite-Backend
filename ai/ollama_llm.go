package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaLLMClient talks to a self-hosted Ollama instance's chat endpoint
// (POST /api/chat) and satisfies LLMClient — the only chat backend
// StoneSuite uses: fully self-hosted, no third-party API key, no external
// quota, at the cost of running on the same small box as the embedder (see
// ollama/fly.toml).
type OllamaLLMClient struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// maxPredictTokens bounds how many tokens the model may generate per answer.
// A CPU-bound box has no fast path — worst-case generation time scales
// directly with output length, and an unbounded response risks the request
// running past Fly's own proxy timeout regardless of how quick the model
// starts responding. 300 tokens is plenty for a grounded, cited answer.
const maxPredictTokens = 300

// NewOllamaLLMClient builds a chat client against the given self-hosted
// Ollama instance and model tag (e.g. "llama3.2:1b").
func NewOllamaLLMClient(baseURL, model string) *OllamaLLMClient {
	return &OllamaLLMClient{
		baseURL:    baseURL,
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

type ollamaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ollamaChatOptions struct {
	NumPredict int `json:"num_predict"`
}
type ollamaChatReq struct {
	Model    string              `json:"model"`
	Messages []ollamaChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
	Options  ollamaChatOptions   `json:"options"`
}
type ollamaChatResp struct {
	Message ollamaChatMessage `json:"message"`
}

// Chat sends the system prompt + messages to Ollama's /api/chat and returns
// the assistant message's content. stream:false so the full reply comes back
// in one JSON object instead of newline-delimited chunks.
func (c *OllamaLLMClient) Chat(ctx context.Context, system string, messages []Message) (string, error) {
	reqMessages := make([]ollamaChatMessage, 0, len(messages)+1)
	if system != "" {
		reqMessages = append(reqMessages, ollamaChatMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		reqMessages = append(reqMessages, ollamaChatMessage(m))
	}
	body := ollamaChatReq{
		Model:    c.model,
		Messages: reqMessages,
		Stream:   false,
		Options:  ollamaChatOptions{NumPredict: maxPredictTokens},
	}
	url := c.baseURL + "/api/chat"

	var out ollamaChatResp
	if err := c.postJSON(ctx, url, body, &out); err != nil {
		return "", fmt.Errorf("ollama chat: %w", err)
	}
	if out.Message.Content == "" {
		return "", fmt.Errorf("ollama chat: empty response")
	}
	return out.Message.Content, nil
}

// postJSON marshals body, POSTs it, and decodes a 2xx JSON response into out.
// Non-2xx responses become errors that include the status code.
func (c *OllamaLLMClient) postJSON(ctx context.Context, url string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

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

const geminiDefaultBaseURL = "https://generativelanguage.googleapis.com"

// GeminiClient talks to the Google Gemini REST API and satisfies LLMClient
// (chat only; embeddings are handled by OllamaEmbedder per ADR-001).
// Free-tier friendly: no SDK, plain HTTP.
type GeminiClient struct {
	apiKey     string
	chatModel  string
	baseURL    string
	httpClient *http.Client
}

// NewGeminiClient builds a chat client for the given API key and chat model.
func NewGeminiClient(apiKey, chatModel string) *GeminiClient {
	return &GeminiClient{
		apiKey:     apiKey,
		chatModel:  chatModel,
		baseURL:    geminiDefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ---- Chat ----

type geminiPart struct {
	Text string `json:"text"`
}
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}
type geminiGenerateReq struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
}
type geminiGenerateResp struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

// Chat sends the system prompt + messages to generateContent and returns the
// first candidate's text.
func (c *GeminiClient) Chat(ctx context.Context, system string, messages []Message) (string, error) {
	contents := make([]geminiContent, 0, len(messages))
	for _, m := range messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}})
	}
	body := geminiGenerateReq{Contents: contents}
	if system != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: system}}}
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, c.chatModel, c.apiKey)

	var out geminiGenerateResp
	if err := c.postJSON(ctx, url, body, &out); err != nil {
		return "", fmt.Errorf("gemini chat: %w", err)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini chat: empty response")
	}
	return out.Candidates[0].Content.Parts[0].Text, nil
}

// postJSON marshals body, POSTs it, and decodes a 2xx JSON response into out.
// Non-2xx responses become errors that include the status code.
func (c *GeminiClient) postJSON(ctx context.Context, url string, body, out any) error {
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
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

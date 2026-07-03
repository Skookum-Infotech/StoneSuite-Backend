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

const groqDefaultBaseURL = "https://api.groq.com"

// GroqClient talks to Groq's OpenAI-compatible chat completions API and
// satisfies LLMClient. A free-tier alternate to GeminiClient, selected via
// AI_LLM_PROVIDER=groq (see NewLLM).
type GroqClient struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewGroqClient builds a chat client for the given API key and model.
func NewGroqClient(apiKey, model string) *GroqClient {
	return &GroqClient{
		apiKey:     apiKey,
		model:      model,
		baseURL:    groqDefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type groqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type groqChatReq struct {
	Model    string        `json:"model"`
	Messages []groqMessage `json:"messages"`
}
type groqChatResp struct {
	Choices []struct {
		Message groqMessage `json:"message"`
	} `json:"choices"`
}

// Chat sends the system prompt + messages to Groq's chat completions endpoint
// and returns the first choice's content. Roles map directly: StoneSuite's
// "assistant" already matches OpenAI-style role naming (unlike Gemini's "model").
func (c *GroqClient) Chat(ctx context.Context, system string, messages []Message) (string, error) {
	reqMessages := make([]groqMessage, 0, len(messages)+1)
	if system != "" {
		reqMessages = append(reqMessages, groqMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		reqMessages = append(reqMessages, groqMessage{Role: m.Role, Content: m.Content})
	}
	body := groqChatReq{Model: c.model, Messages: reqMessages}
	url := c.baseURL + "/openai/v1/chat/completions"

	var out groqChatResp
	if err := c.postJSON(ctx, url, body, &out); err != nil {
		return "", fmt.Errorf("groq chat: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("groq chat: empty response")
	}
	return out.Choices[0].Message.Content, nil
}

// postJSON marshals body, POSTs it with bearer auth, and decodes a 2xx JSON
// response into out. Non-2xx responses become errors that include the status.
func (c *GroqClient) postJSON(ctx context.Context, url string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

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

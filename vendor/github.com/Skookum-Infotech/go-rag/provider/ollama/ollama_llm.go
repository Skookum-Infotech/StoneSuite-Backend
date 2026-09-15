package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// LLMClient talks to a self-hosted Ollama instance's chat endpoint
// (POST /api/chat) and satisfies LLMClient — the only chat backend
// StoneSuite uses: fully self-hosted, no third-party API key, no external
// quota, at the cost of running on the same small box as the embedder (see
// ollama/fly.toml).
type LLMClient struct {
	baseURL    string
	model      string
	httpClient *http.Client
	// streamClient has no request timeout: a legitimate streamed generation
	// can run for minutes, and httpClient's fixed Timeout covers the whole
	// body read — which would guillotine a stream mid-flight. Bounded
	// instead by the caller's context deadline plus streamIdleTimeout below.
	streamClient *http.Client
}

// maxPredictTokens bounds how many tokens the model may generate per answer.
// A CPU-bound box has no fast path — worst-case generation time scales
// directly with output length, and an unbounded response risks the request
// running past Fly's own proxy timeout regardless of how quick the model
// starts responding. 300 tokens is plenty for a grounded, cited answer.
const maxPredictTokens = 300

// chatTimeout bounds a non-streaming Chat/ChatJSON call. Must stay under the
// caller's own write/proxy timeout with margin — StoneSuite-Backend's
// main.go documents this client's timeout in its own comment and sizes its
// http.Server.WriteTimeout (120s) to exceed it; change the two together.
const chatTimeout = 100 * time.Second

// streamIdleTimeout bounds how long ChatStream waits for the *next* chunk
// once streaming has started (not the whole call — a long answer legitimately
// takes longer than this in total). Guards against a connection that goes
// silent mid-stream without the server ever closing it.
const streamIdleTimeout = 30 * time.Second

// NewLLMClient builds a chat client against the given self-hosted
// Ollama instance and model tag (e.g. "llama3.2:1b").
func NewLLMClient(baseURL, model string) *LLMClient {
	return &LLMClient{
		baseURL:      baseURL,
		model:        model,
		httpClient:   &http.Client{Timeout: chatTimeout},
		streamClient: &http.Client{},
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
	// Format is Ollama's structured-output constraint: omitted for a plain
	// Chat call, and a raw JSON Schema for ChatJSON. json.RawMessage so this
	// package never has to parse or validate the schema it forwards.
	Format  json.RawMessage   `json:"format,omitempty"`
	Options ollamaChatOptions `json:"options"`
}
type ollamaChatResp struct {
	Message ollamaChatMessage `json:"message"`
}

// Chat sends the system prompt + messages to Ollama's /api/chat and returns
// the assistant message's content. stream:false so the full reply comes back
// in one JSON object instead of newline-delimited chunks.
func (c *LLMClient) Chat(ctx context.Context, system string, messages []rag.Message) (string, error) {
	return c.chat(ctx, system, messages, nil)
}

// ChatJSON satisfies rag.StructuredLLMClient: it sends the same request as
// Chat but with Ollama's "format" field set to schema, constraining the
// decoder to emit only JSON matching it.
func (c *LLMClient) ChatJSON(ctx context.Context, system string, messages []rag.Message, schema []byte) (string, error) {
	return c.chat(ctx, system, messages, schema)
}

type ollamaChatStreamChunk struct {
	Message ollamaChatMessage `json:"message"`
	Done    bool              `json:"done"`
	// Ollama's final chunk (done:true) carries an "error" field instead of a
	// message when generation itself failed partway through (not an HTTP
	// error — the request already returned 200 and started streaming).
	Error string `json:"error,omitempty"`
}

// scanResult carries one line's decode outcome from the background scan
// goroutine in ChatStream back to the select loop that applies the idle
// timeout. buffered (cap 1): the goroutine never blocks sending even if the
// loop has already returned on a timeout/cancellation.
type scanResult struct {
	chunk ollamaChatStreamChunk
	err   error // io.EOF on a clean end of stream
}

// ChatStream satisfies rag.StreamingLLMClient: it sends the same request as
// Chat but with stream:true, decoding Ollama's newline-delimited JSON chunks
// and forwarding each one's message content to onToken as it arrives.
//
// Uses streamClient (no fixed Timeout) so a long generation isn't guillotined
// by a whole-body deadline; bounded instead by ctx and by streamIdleTimeout,
// which aborts if the *next* line doesn't arrive within that window — reading
// happens on a background goroutine so the timeout can fire even while a
// Scan() call is blocked on the connection.
func (c *LLMClient) ChatStream(ctx context.Context, system string, messages []rag.Message, onToken func(string) error) (string, error) {
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
		Stream:   true,
		Options:  ollamaChatOptions{NumPredict: maxPredictTokens},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("ollama chat stream: marshal: %w", err)
	}

	// A child context lets the idle-timeout branch abort the in-flight
	// request (unblocking the background scan goroutine's read) without
	// otherwise touching the caller's own context.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("ollama chat stream: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama chat stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama chat stream: status %d: %s", resp.StatusCode, string(respBody))
	}

	scanner := bufio.NewScanner(resp.Body)
	// Ollama's default buffer holds one small JSON object per line; widen it
	// so a chunk with an unusually long single message content never trips
	// bufio.ErrTooLong.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	results := make(chan scanResult, 1)
	scanNext := func() {
		if !scanner.Scan() {
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			results <- scanResult{err: err}
			return
		}
		var chunk ollamaChatStreamChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			results <- scanResult{err: fmt.Errorf("decode: %w", err)}
			return
		}
		results <- scanResult{chunk: chunk}
	}

	var answer strings.Builder
	go scanNext()
	for {
		timer := time.NewTimer(streamIdleTimeout)
		select {
		case res := <-results:
			timer.Stop()
			if res.err != nil {
				if errors.Is(res.err, io.EOF) {
					if answer.Len() == 0 {
						return "", fmt.Errorf("ollama chat stream: empty response")
					}
					return answer.String(), nil
				}
				return answer.String(), fmt.Errorf("ollama chat stream: %w", res.err)
			}
			if res.chunk.Error != "" {
				return answer.String(), fmt.Errorf("ollama chat stream: %s", res.chunk.Error)
			}
			if res.chunk.Message.Content != "" {
				answer.WriteString(res.chunk.Message.Content)
				if err := onToken(res.chunk.Message.Content); err != nil {
					return answer.String(), fmt.Errorf("onToken: %w", err)
				}
			}
			if res.chunk.Done {
				if answer.Len() == 0 {
					return "", fmt.Errorf("ollama chat stream: empty response")
				}
				return answer.String(), nil
			}
			go scanNext()
		case <-timer.C:
			cancel()
			return answer.String(), fmt.Errorf("ollama chat stream: idle for %s waiting on the next chunk", streamIdleTimeout)
		case <-ctx.Done():
			timer.Stop()
			return answer.String(), ctx.Err()
		}
	}
}

func (c *LLMClient) chat(ctx context.Context, system string, messages []rag.Message, format json.RawMessage) (string, error) {
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
		Format:   format,
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
func (c *LLMClient) postJSON(ctx context.Context, url string, body, out any) error {
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

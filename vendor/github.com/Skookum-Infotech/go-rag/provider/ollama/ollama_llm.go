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
	keepAlive  string
	httpClient *http.Client
	// streamClient has no request timeout: a legitimate streamed generation
	// can run for minutes, and httpClient's fixed Timeout covers the whole
	// body read — which would guillotine a stream mid-flight. Bounded
	// instead by the caller's context deadline plus the first-token and idle
	// timeouts below.
	streamClient *http.Client
	retryDelay   time.Duration
	// firstTokenTimeout/idleTimeout are fields (defaulted from the consts) so
	// tests can shrink them without waiting out the real values.
	firstTokenTimeout time.Duration
	idleTimeout       time.Duration
}

// maxPredictTokens bounds how many tokens the model may generate per answer.
// A CPU-bound box has no fast path — worst-case generation time scales
// directly with output length, and an unbounded response risks the request
// running past Fly's own proxy timeout regardless of how quick the model
// starts responding. 300 tokens is plenty for a grounded, cited answer; an
// answer that hits it is reported via rag.Usage.Truncated.
const maxPredictTokens = 300

// contextWindow is the num_ctx sent with every request. Ollama's own default
// is small enough that a system prompt + retrieved context + a few history
// turns can overflow it, and Ollama then drops the OLDEST tokens silently —
// which is the system prompt. 4096 fits llama3.2 comfortably on this box.
const contextWindow = 4096

// answerTemperature keeps grounded answers close to the retrieved text;
// routingTemperature makes schema-constrained extraction deterministic.
const (
	answerTemperature  = 0.2
	routingTemperature = 0.0
)

// chatTimeout bounds a non-streaming Chat/ChatJSON call. 100s, not 60s: on
// the CPU-only iad box, prefill alone runs at ~20-25 tok/s, so a full 800+
// token context (system prompt + top-K retrieved chunks + question) can burn
// 30-40s before generation even starts — at 60s a legitimately in-progress
// answer was hard-failing with a cancelled request instead of finishing a
// few seconds later. Must stay under the caller's own write/proxy timeout
// with margin — StoneSuite-Backend's main.go documents this client's
// timeout in its own comment and sizes its http.Server.WriteTimeout (120s)
// to exceed it; change the two together.
const chatTimeout = 100 * time.Second

// streamFirstTokenTimeout bounds the wait for the FIRST chunk of a stream.
// Ollama emits nothing until prefill finishes, and prefill on the CPU box
// takes 30-40s for a full context — so the first chunk needs the same budget
// as a whole non-streaming call, not the short between-chunk idle window.
const streamFirstTokenTimeout = chatTimeout

// streamIdleTimeout bounds how long ChatStream waits for the *next* chunk
// once streaming has started (not the whole call — a long answer legitimately
// takes longer than this in total). Guards against a connection that goes
// silent mid-stream without the server ever closing it.
const streamIdleTimeout = 30 * time.Second

// chatRetries bounds retries of a chat request that failed before producing
// anything — connection refused/reset while the box autostarts, or a
// 502/503/504 from the proxy in front of it. Never retried once a stream has
// delivered a token: a resend would duplicate text the caller already showed.
const chatRetries = 2

// defaultRetryDelay is the base backoff between chat retries (doubled each
// attempt).
const defaultRetryDelay = time.Second

// maxErrorBody caps how much of a non-2xx response body is read into an
// error message.
const maxErrorBody = 4 << 10

// NewLLMClient builds a chat client against the given self-hosted
// Ollama instance and model tag (e.g. "llama3.2:3b").
func NewLLMClient(baseURL, model string) *LLMClient {
	return &LLMClient{
		baseURL:           baseURL,
		model:             model,
		httpClient:        &http.Client{Timeout: chatTimeout},
		streamClient:      &http.Client{},
		retryDelay:        defaultRetryDelay,
		firstTokenTimeout: streamFirstTokenTimeout,
		idleTimeout:       streamIdleTimeout,
	}
}

// WithKeepAlive sets Ollama's keep_alive (e.g. "30m", "-1") so the model stays
// loaded between requests instead of paying a reload on the next question.
// Empty leaves Ollama's server default.
func (c *LLMClient) WithKeepAlive(keepAlive string) *LLMClient {
	c.keepAlive = keepAlive
	return c
}

type ollamaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ollamaChatOptions struct {
	NumPredict  int     `json:"num_predict"`
	NumCtx      int     `json:"num_ctx"`
	Temperature float64 `json:"temperature"`
}
type ollamaChatReq struct {
	Model    string              `json:"model"`
	Messages []ollamaChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
	// Format is Ollama's structured-output constraint: omitted for a plain
	// Chat call, and a raw JSON Schema for ChatJSON. json.RawMessage so this
	// package never has to parse or validate the schema it forwards.
	Format    json.RawMessage   `json:"format,omitempty"`
	KeepAlive string            `json:"keep_alive,omitempty"`
	Options   ollamaChatOptions `json:"options"`
}

// ollamaChatResp is both the non-streaming reply and one streamed chunk: the
// final chunk (done:true) carries done_reason and the token counts.
type ollamaChatResp struct {
	Message         ollamaChatMessage `json:"message"`
	Done            bool              `json:"done"`
	DoneReason      string            `json:"done_reason,omitempty"`
	PromptEvalCount int               `json:"prompt_eval_count,omitempty"`
	EvalCount       int               `json:"eval_count,omitempty"`
	// Ollama's final chunk carries an "error" field instead of a message
	// when generation itself failed partway through (not an HTTP error — the
	// request already returned 200 and started streaming).
	Error string `json:"error,omitempty"`
}

// usage converts a final chunk's accounting into rag.Usage.
func (r ollamaChatResp) usage() rag.Usage {
	return rag.Usage{
		PromptTokens:     r.PromptEvalCount,
		CompletionTokens: r.EvalCount,
		Truncated:        r.DoneReason == "length",
	}
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

// buildRequest assembles the /api/chat body shared by every call shape.
func (c *LLMClient) buildRequest(system string, messages []rag.Message, stream bool, format json.RawMessage) ollamaChatReq {
	reqMessages := make([]ollamaChatMessage, 0, len(messages)+1)
	if system != "" {
		reqMessages = append(reqMessages, ollamaChatMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		reqMessages = append(reqMessages, ollamaChatMessage(m))
	}
	temperature := answerTemperature
	if format != nil {
		temperature = routingTemperature
	}
	return ollamaChatReq{
		Model:     c.model,
		Messages:  reqMessages,
		Stream:    stream,
		Format:    format,
		KeepAlive: c.keepAlive,
		Options: ollamaChatOptions{
			NumPredict:  maxPredictTokens,
			NumCtx:      contextWindow,
			Temperature: temperature,
		},
	}
}

// scanResult carries one line's decode outcome from the background scan
// goroutine in ChatStream back to the select loop that applies the timeouts.
// buffered (cap 1): the goroutine never blocks sending even if the loop has
// already returned on a timeout/cancellation.
type scanResult struct {
	chunk ollamaChatResp
	err   error // io.EOF on a clean end of stream
}

// ChatStream satisfies rag.StreamingLLMClient: it sends the same request as
// Chat but with stream:true, decoding Ollama's newline-delimited JSON chunks
// and forwarding each one's message content to onToken as it arrives.
//
// Uses streamClient (no fixed Timeout) so a long generation isn't guillotined
// by a whole-body deadline. Bounded instead by ctx, by firstTokenTimeout
// until the first chunk arrives (prefill), and by idleTimeout between chunks
// after that — reading happens on a background goroutine so a timeout can
// fire even while a Scan() call is blocked on the connection.
func (c *LLMClient) ChatStream(ctx context.Context, system string, messages []rag.Message, onToken func(string) error) (string, error) {
	buf, err := json.Marshal(c.buildRequest(system, messages, true, nil))
	if err != nil {
		return "", fmt.Errorf("ollama chat stream: marshal: %w", err)
	}

	// A child context lets a timeout branch abort the in-flight request
	// (unblocking the background scan goroutine's read) without otherwise
	// touching the caller's own context.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resp, err := c.doWithRetry(streamCtx, c.streamClient, buf)
	if err != nil {
		return "", fmt.Errorf("ollama chat stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
		var chunk ollamaChatResp
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			results <- scanResult{err: fmt.Errorf("decode: %w", err)}
			return
		}
		results <- scanResult{chunk: chunk}
	}

	var answer strings.Builder
	wait := c.firstTokenTimeout
	go scanNext()
	for {
		timer := time.NewTimer(wait)
		select {
		case res := <-results:
			timer.Stop()
			wait = c.idleTimeout
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
				rag.ReportUsage(ctx, res.chunk.usage())
				if answer.Len() == 0 {
					return "", fmt.Errorf("ollama chat stream: empty response")
				}
				return answer.String(), nil
			}
			go scanNext()
		case <-timer.C:
			cancel()
			phase := "the next chunk"
			if answer.Len() == 0 {
				phase = "the first token"
			}
			return answer.String(), fmt.Errorf("ollama chat stream: no response for %s waiting on %s: %w", wait, phase, context.DeadlineExceeded)
		case <-ctx.Done():
			timer.Stop()
			return answer.String(), ctx.Err()
		}
	}
}

func (c *LLMClient) chat(ctx context.Context, system string, messages []rag.Message, format json.RawMessage) (string, error) {
	buf, err := json.Marshal(c.buildRequest(system, messages, false, format))
	if err != nil {
		return "", fmt.Errorf("ollama chat: marshal: %w", err)
	}
	resp, err := c.doWithRetry(ctx, c.httpClient, buf)
	if err != nil {
		return "", fmt.Errorf("ollama chat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out ollamaChatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ollama chat: decode: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama chat: %s", out.Error)
	}
	if out.Message.Content == "" {
		return "", fmt.Errorf("ollama chat: empty response")
	}
	rag.ReportUsage(ctx, out.usage())
	return out.Message.Content, nil
}

// doWithRetry POSTs body to /api/chat and returns a 2xx response for the
// caller to read and close. Failures that are expected to clear on their own
// (transport errors, 502/503/504) are retried with backoff and, once retries
// run out, wrapped as rag.ErrUnavailable; a 404 is rag.ErrModelNotFound.
// A caller-side cancellation or deadline is returned as-is, never retried.
func (c *LLMClient) doWithRetry(ctx context.Context, client *http.Client, body []byte) (*http.Response, error) {
	delay := c.retryDelay
	for attempt := 0; ; attempt++ {
		resp, err := c.doOnce(ctx, client, body)
		if err == nil || !errors.Is(err, rag.ErrUnavailable) || attempt >= chatRetries {
			return resp, err
		}
		if sleepErr := sleepOrDone(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
		delay *= 2
	}
}

func (c *LLMClient) doOnce(ctx context.Context, client *http.Client, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if isTimeout(err) {
			return nil, fmt.Errorf("do request: %w", context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("do request: %w: %w", rag.ErrUnavailable, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return nil, statusError(resp.StatusCode, msg)
}

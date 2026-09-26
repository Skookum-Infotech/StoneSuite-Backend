package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// requestTimeout bounds a single ask/stream call — comfortably above the
// server's own streamMaxDuration (5m) ceiling so a genuinely wedged
// connection still resolves rather than us guessing at a tighter number.
const requestTimeout = 5*time.Minute + 10*time.Second

// maxStatusBodyBytes caps how much of a non-stream (error) response body we
// read back.
const maxStatusBodyBytes = 64 << 10

// sseScannerBufferBytes is the max single-line size the SSE scanner accepts —
// well above any one token/done/error frame's JSON, including a citations
// list.
const sseScannerBufferBytes = 1 << 20

// apiClient talks to a deployed StoneSuite backend as an authenticated
// tenant user. The bearer token is never logged or printed.
type apiClient struct {
	base  string
	token string
	hc    *http.Client
	// sleep waits between a 429 and its single retry; injectable for tests.
	sleep func(context.Context, time.Duration)
}

// newAPIClient builds a client against base ("https://host") using token as
// the bearer credential for every request.
func newAPIClient(base, token string) *apiClient {
	return &apiClient{base: strings.TrimSuffix(base, "/"), token: token, hc: &http.Client{}, sleep: sleepCtx}
}

// askRequestBody mirrors controllers.askRequestBody — the wire shape
// POST /api/tenant/ai/ask/stream expects.
type askRequestBody struct {
	Question       string `json:"question"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// newCSRFToken returns a random hex string for the double-submit CSRF check
// (Cookie: csrf_token=<v> plus X-CSRF-Token: <v>, see middleware/csrf.go).
func newCSRFToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// authedRequest builds an HTTP request carrying the bearer token and, for a
// state-changing method, the double-submit CSRF cookie/header pair.
func (c *apiClient) authedRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut || method == http.MethodDelete {
		csrf, err := newCSRFToken()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Cookie", "csrf_token="+csrf)
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return req, nil
}

// ask runs POST /api/tenant/ai/ask/stream for one question and blocks until
// the stream reaches "done"/"error", the connection closes, or ctx expires.
func (c *apiClient) ask(ctx context.Context, question, conversationID string) askResult {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	body, err := json.Marshal(askRequestBody{Question: question, ConversationID: conversationID})
	if err != nil {
		return askResult{TransportErr: fmt.Errorf("marshal ask request: %w", err)}
	}
	req, err := c.authedRequest(ctx, http.MethodPost, "/api/tenant/ai/ask/stream", body)
	if err != nil {
		return askResult{TransportErr: err}
	}
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return askResult{TransportErr: fmt.Errorf("ask request: %w", err), Total: time.Since(start)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return c.readStatusFailure(resp, start)
	}
	return readSSEStream(resp.Body, start)
}

// readStatusFailure reads a non-streaming (ordinary JSON error) response
// body, for the ask calls that fail before any SSE header is written
// (auth/validation/permission checks in prepareAsk).
func (c *apiClient) readStatusFailure(resp *http.Response, start time.Time) askResult {
	limited := io.LimitReader(resp.Body, maxStatusBodyBytes)
	raw, _ := io.ReadAll(limited)
	res := askResult{HTTPStatus: resp.StatusCode, Total: time.Since(start)}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		res.StatusBodyRaw = parsed
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		res.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), parsed)
	}
	return res
}

// maxRetryWait caps how long a 429 retry will wait.
const maxRetryWait = 30 * time.Second

// parseRetryAfter reads the wait (seconds) from the Retry-After header, else
// the body's retryAfter field. Returns zero when neither is usable.
func parseRetryAfter(header string, body map[string]any) time.Duration {
	if n, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	if n, ok := body["retryAfter"].(float64); ok && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	return 0
}

// sleepCtx waits d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// askRetry runs ask and, on a 429, waits the server-suggested time (capped at
// maxRetryWait, minimum one second) and retries once.
func (c *apiClient) askRetry(ctx context.Context, question, conversationID string) askResult {
	res := c.ask(ctx, question, conversationID)
	if res.HTTPStatus != http.StatusTooManyRequests {
		return res
	}
	wait := res.RetryAfter
	if wait < time.Second {
		wait = time.Second
	}
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	c.sleep(ctx, wait)
	return c.ask(ctx, question, conversationID)
}

// createConversation runs POST /api/tenant/ai/conversations and returns the
// new conversation id.
func (c *apiClient) createConversation(ctx context.Context) (string, error) {
	req, err := c.authedRequest(ctx, http.MethodPost, "/api/tenant/ai/conversations", []byte("{}"))
	if err != nil {
		return "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBodyBytes))
	if err != nil {
		return "", fmt.Errorf("read create conversation response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create conversation: status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode create conversation response: %w", err)
	}
	if out.Data.ID == "" {
		return "", fmt.Errorf("create conversation: empty id in response")
	}
	return out.Data.ID, nil
}

// readSSEStream reads a 200 text/event-stream body to completion, timing
// time-to-first-token and total time, and folding "token" frames into the
// answer and the terminal "done"/"error" frame into the rest of the result.
func readSSEStream(body io.Reader, start time.Time) askResult {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseScannerBufferBytes)

	var res askResult
	res.HTTPStatus = http.StatusOK
	var answer strings.Builder
	var ttftSet bool
	acc := &sseAccumulator{}

	for scanner.Scan() {
		f, ok := acc.feed(strings.TrimSuffix(scanner.Text(), "\r"))
		if !ok {
			continue
		}
		switch f.Event {
		case "token":
			if !ttftSet {
				res.TTFT = time.Since(start)
				ttftSet = true
			}
			tok, err := decodeTokenData(f.Data)
			if err != nil {
				res.TransportErr = err
				res.Total = time.Since(start)
				return res
			}
			answer.WriteString(tok)
		case "sources":
			obj, err := decodeObjectData(f.Data)
			if err == nil {
				res.SourcesCount = citationsLen(obj)
			}
		case "done":
			obj, err := decodeObjectData(f.Data)
			if err != nil {
				res.TransportErr = err
				res.Total = time.Since(start)
				return res
			}
			res.Done = obj
			if a, ok := obj["answer"].(string); ok && a != "" {
				res.Answer = a
			} else {
				res.Answer = answer.String()
			}
			if n := citationsLen(obj); n > 0 {
				res.SourcesCount = n
			}
			if cid, ok := obj["conversation_id"].(string); ok {
				res.ConversationID = cid
			}
			if route, ok := obj["route"].(string); ok {
				res.Route = route
			}
			res.Total = time.Since(start)
			return res
		case "error":
			obj, err := decodeObjectData(f.Data)
			if err != nil {
				res.TransportErr = err
			} else {
				res.ErrorPayload = obj
			}
			res.Answer = answer.String()
			res.Total = time.Since(start)
			return res
		}
	}

	// Stream ended (EOF) without a terminal frame — either a scanner error or
	// the connection was closed early.
	res.Answer = answer.String()
	res.Total = time.Since(start)
	if err := scanner.Err(); err != nil {
		res.TransportErr = fmt.Errorf("read sse stream: %w", err)
	} else {
		res.TransportErr = fmt.Errorf("sse stream ended without a done or error frame")
	}
	return res
}

// citationsLen returns len(obj["citations"]) when that key holds a JSON
// array, else 0.
func citationsLen(obj map[string]any) int {
	cites, ok := obj["citations"].([]any)
	if !ok {
		return 0
	}
	return len(cites)
}

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// embedPrefixes are one model's task prefixes. Retrieval models are trained to
// see a short instruction before the text, and which instruction differs per
// model family. The pair MUST be applied consistently or query and document
// vectors stop being comparable (see ADR-001) — which is why they are looked up
// together, keyed by the same model name, rather than configured separately.
type embedPrefixes struct{ doc, query string }

// modelPrefixes maps an Ollama model name (tag stripped) to its task prefixes.
//
// Getting this wrong is silent: a wrong-but-consistent prefix still returns
// plausible vectors, it just degrades recall, so there is no error to notice.
// Add an entry here when introducing a model rather than leaving it to the
// unknown-model fallback.
var modelPrefixes = map[string]embedPrefixes{
	// nomic asks for symmetric "search_document:"/"search_query:" markers.
	"nomic-embed-text": {doc: "search_document: ", query: "search_query: "},
	// arctic-embed embeds documents bare and instructs only the query side.
	"snowflake-arctic-embed":  {doc: "", query: "Represent this sentence for searching relevant passages: "},
	"snowflake-arctic-embed2": {doc: "", query: "query: "},
	// bge-m3 and mxbai are trained without task prefixes.
	"bge-m3":            {},
	"mxbai-embed-large": {},
}

// prefixesFor resolves a model tag (e.g. "snowflake-arctic-embed:m") to its
// prefixes. An unrecognised model falls back to no prefixes rather than an
// error: empty is *safe* (doc and query stay mutually consistent, losing only
// the model's task-tuning), whereas guessing another family's prefix actively
// corrupts recall, and failing hard would block boot on a perfectly valid model
// that simply is not listed yet.
func prefixesFor(model string) embedPrefixes {
	base, _, _ := strings.Cut(model, ":")
	if p, ok := modelPrefixes[base]; ok {
		return p
	}
	slog.Warn("no task prefixes registered for embedding model; using none",
		"model", model, "known", len(modelPrefixes))
	return embedPrefixes{}
}

// maxEmbedBatch caps how many texts go in one /api/embed call. The embedder box
// is CPU-bound with modest RAM, so an unbounded batch trades one slow request
// for a memory spike; 32 keeps the round-trip win without that risk.
const maxEmbedBatch = 32

// OllamaEmbedder embeds text via a self-hosted Ollama instance (POST
// /api/embed). It satisfies Embedder. The task prefix is fixed at construction
// so call sites never have to remember it.
type OllamaEmbedder struct {
	baseURL    string
	model      string
	prefix     string
	dim        int // expected vector width; 0 disables the check
	httpClient *http.Client
	retryDelay time.Duration // overridable by tests; see transportRetries
}

// NewOllamaDocEmbedder builds an embedder for STORED text. Use it in the
// ingestion worker. dim is the expected vector width (config.AIEmbedDim); pass
// 0 to skip validation.
func NewOllamaDocEmbedder(baseURL, model string, dim int) *OllamaEmbedder {
	return newOllamaEmbedder(baseURL, model, prefixesFor(model).doc, dim)
}

// NewOllamaQueryEmbedder builds an embedder for QUESTIONS. Use it in the
// retriever. dim is the expected vector width; pass 0 to skip validation.
func NewOllamaQueryEmbedder(baseURL, model string, dim int) *OllamaEmbedder {
	return newOllamaEmbedder(baseURL, model, prefixesFor(model).query, dim)
}

func newOllamaEmbedder(baseURL, model, prefix string, dim int) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL:    baseURL,
		model:      model,
		prefix:     prefix,
		dim:        dim,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		retryDelay: 2 * time.Second,
	}
}

// Fingerprint names the vector space this embedder produces. Both the model
// and the task prefix belong in it: the same model with a different prefix
// yields vectors that are not comparable with previously stored ones, which is
// exactly the change that must invalidate stored hashes and force a re-embed.
func (e *OllamaEmbedder) Fingerprint() string { return e.model + "\x00" + e.prefix }

type ollamaEmbedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}
type ollamaEmbedResp struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns one vector per input text, in order, each prefixed with this
// embedder's task prefix. Texts are sent in batches of maxEmbedBatch — one
// round-trip per batch rather than per text, which is what makes a full reindex
// tolerable on a CPU-bound box.
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxEmbedBatch {
		end := min(start+maxEmbedBatch, len(texts))
		vecs, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("ollama embed[%d:%d]: %w", start, end, err)
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// embedBatch sends one /api/embed call and validates the response shape. A
// short response or a wrong-width vector is an error here rather than at the
// database write, so a model/schema mismatch surfaces with the model name
// attached instead of as an opaque pgvector insert failure.
func (e *OllamaEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	input := make([]string, len(texts))
	for i, t := range texts {
		input[i] = e.prefix + t
	}

	var resp ollamaEmbedResp
	if err := e.postJSON(ctx, e.baseURL+"/api/embed", ollamaEmbedReq{Model: e.model, Input: input}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("model %s returned %d embeddings for %d inputs", e.model, len(resp.Embeddings), len(texts))
	}
	for i, v := range resp.Embeddings {
		if len(v) == 0 {
			return nil, fmt.Errorf("model %s returned an empty embedding at %d", e.model, i)
		}
		if e.dim > 0 && len(v) != e.dim {
			return nil, fmt.Errorf("model %s returned %d dimensions, expected %d (AI_EMBED_DIM must match the vector(N) column)", e.model, len(v), e.dim)
		}
	}
	return resp.Embeddings, nil
}

// transportRetries bounds retries for connection-level failures only (refused/
// reset/EOF/DNS lookup failure) — the window where the self-hosted embedder
// box is mid autostart/autostop under scale-to-zero. Application errors
// (non-2xx status) are not retried here; those need a human, not a resend.
//
// 5 retries at retryDelay (2s) gives ~10s of budget: observed live, a fully
// cold Ollama Machine (backend just woke from scale-to-zero, Ollama's own
// Machine not yet DNS-reachable) can take several seconds longer to become
// reachable than the previous 2-retry/~4s budget covered, causing the very
// first request after an idle period to fail outright with "no such host".
const transportRetries = 5

// postJSON marshals body, POSTs it, and decodes a 2xx JSON response into out.
// Non-2xx responses become errors that include the status code.
func (e *OllamaEmbedder) postJSON(ctx context.Context, url string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
		if err != nil {
			return fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err = e.httpClient.Do(req)
		if err == nil {
			break
		}
		if attempt >= transportRetries {
			return fmt.Errorf("do request: %w", err)
		}
		if sleepErr := sleepOrDone(ctx, e.retryDelay); sleepErr != nil {
			return fmt.Errorf("do request: %w", err)
		}
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

// sleepOrDone waits d unless ctx is cancelled first, in which case it returns
// ctx.Err() immediately instead of blocking out the full delay.
func sleepOrDone(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Package tei talks to a self-hosted Hugging Face Text Embeddings Inference
// (TEI) server — https://github.com/huggingface/text-embeddings-inference —
// for embedding and reranking. Ollama serves generation well but is not a
// purpose-built embedding/rerank server: TEI batches requests properly and
// ships a dedicated cross-encoder /rerank endpoint that Ollama has no
// equivalent of, which is why it runs alongside Ollama rather than
// replacing it.
package tei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// client is the shared HTTP plumbing. A single TEI server is configured at
// startup to serve EITHER embedding OR reranking, not both — that's how the
// upstream project ships its Docker images — so Embedder and Reranker each
// hold their own client pointed at their own deployment's baseURL.
type client struct {
	baseURL    string
	httpClient *http.Client
	retryDelay time.Duration
}

func newClient(baseURL string) *client {
	return &client{baseURL: baseURL, httpClient: &http.Client{Timeout: 30 * time.Second}, retryDelay: time.Second}
}

// transportRetries bounds retries for connection-level failures only
// (refused/reset/EOF) — mirrors provider/ollama's retry rationale for a box
// that scale-to-zero can leave briefly unreachable right after autostart.
const transportRetries = 3

// postJSON marshals body, POSTs it to baseURL+path, and decodes a 2xx JSON
// response into out. Non-2xx responses become errors carrying the status
// code and body, so a misconfigured model or a malformed request is
// diagnosable from the error alone.
func (c *client) postJSON(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
		if rerr != nil {
			return fmt.Errorf("new request: %w", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = c.httpClient.Do(req)
		if err == nil {
			break
		}
		if attempt >= transportRetries {
			return fmt.Errorf("post %s: %w", path, err)
		}
		select {
		case <-time.After(c.retryDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tei %s: status %d: %s", path, resp.StatusCode, string(b))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// Embedder embeds text via a self-hosted TEI instance (POST /embed).
// Satisfies rag.Embedder.
type Embedder struct {
	c      *client
	prefix string
	dim    int // expected vector width; 0 disables the check
}

// NewEmbedder builds a TEI embedder. prefix is a task-instruction prefix to
// prepend to every input — pass "" unless the deployed model's card
// documents one (most TEI-served embedding models, unlike the Ollama
// family in provider/ollama, are trained without one). dim is the expected
// vector width (config.AppConfig.AIEmbedDim); pass 0 to skip validation.
func NewEmbedder(baseURL, prefix string, dim int) *Embedder {
	return &Embedder{c: newClient(baseURL), prefix: prefix, dim: dim}
}

type teiEmbedReq struct {
	Inputs []string `json:"inputs"`
}

// Embed returns one vector per input text, in order. TEI's /embed accepts
// the whole batch in one request (no client-side batch splitting needed
// the way provider/ollama's maxEmbedBatch requires) — batching is TEI's job.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	input := make([]string, len(texts))
	for i, t := range texts {
		input[i] = e.prefix + t
	}

	var out [][]float32
	if err := e.c.postJSON(ctx, "/embed", teiEmbedReq{Inputs: input}, &out); err != nil {
		return nil, fmt.Errorf("tei embed: %w", err)
	}
	if len(out) != len(texts) {
		return nil, fmt.Errorf("tei returned %d embeddings for %d inputs", len(out), len(texts))
	}
	if e.dim > 0 {
		for i, v := range out {
			if len(v) != e.dim {
				return nil, fmt.Errorf("tei returned %d dimensions at %d, expected %d (AI_EMBED_DIM must match the vector(N) column)", len(v), i, e.dim)
			}
		}
	}
	return out, nil
}

// Fingerprint names the vector space this embedder produces — the prefix is
// part of it for the same reason provider/ollama's is: a different prefix
// with the same model yields vectors that are not comparable with
// previously stored ones. baseURL is deliberately excluded — two TEI
// deployments of the identical model/prefix produce the same vector space
// regardless of which host served the request.
func (e *Embedder) Fingerprint() string { return "tei\x00" + e.prefix }

// Reranker cross-encodes a question against each candidate via a
// self-hosted TEI reranking instance (POST /rerank). Satisfies rag.Reranker.
type Reranker struct {
	c *client
}

// NewReranker builds a TEI reranker.
func NewReranker(baseURL string) *Reranker {
	return &Reranker{c: newClient(baseURL)}
}

type teiRerankReq struct {
	Query string   `json:"query"`
	Texts []string `json:"texts"`
}

type teiRerankResult struct {
	Index int     `json:"index"`
	Score float32 `json:"score"`
}

// Rerank scores every candidate against question and returns up to n,
// highest score first. TEI's own response is already score-sorted, but
// Rerank sorts defensively rather than trusting that — a reranker response
// is untrusted network input, and a malformed or adversarial one must fail
// closed (an out-of-range index is an error, not a panic or a silent
// misorder) rather than assume a well-behaved server.
func (r *Reranker) Rerank(ctx context.Context, question string, candidates []rag.Citation, n int) ([]rag.Citation, error) {
	if len(candidates) == 0 || n <= 0 {
		return nil, nil
	}
	texts := make([]string, len(candidates))
	for i, c := range candidates {
		texts[i] = c.Content
	}

	var results []teiRerankResult
	if err := r.c.postJSON(ctx, "/rerank", teiRerankReq{Query: question, Texts: texts}, &results); err != nil {
		return nil, fmt.Errorf("tei rerank: %w", err)
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	out := make([]rag.Citation, 0, min(n, len(results)))
	for _, res := range results {
		if res.Index < 0 || res.Index >= len(candidates) {
			return nil, fmt.Errorf("tei rerank returned out-of-range index %d for %d candidates", res.Index, len(candidates))
		}
		out = append(out, candidates[res.Index])
		if len(out) == n {
			break
		}
	}
	return out, nil
}

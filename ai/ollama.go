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

// nomic-embed-text task prefixes. These MUST be applied consistently or query
// and document vectors stop being comparable (see ADR-001).
const (
	nomicDocPrefix   = "search_document: "
	nomicQueryPrefix = "search_query: "
)

// OllamaEmbedder embeds text via a self-hosted Ollama instance (POST
// /api/embeddings). It satisfies Embedder. The task prefix is fixed at
// construction so call sites never have to remember it.
type OllamaEmbedder struct {
	baseURL    string
	model      string
	prefix     string
	httpClient *http.Client
}

// NewOllamaDocEmbedder builds an embedder for STORED text (search_document:).
// Use it in the ingestion worker.
func NewOllamaDocEmbedder(baseURL, model string) *OllamaEmbedder {
	return newOllamaEmbedder(baseURL, model, nomicDocPrefix)
}

// NewOllamaQueryEmbedder builds an embedder for QUESTIONS (search_query:).
// Use it in the retriever.
func NewOllamaQueryEmbedder(baseURL, model string) *OllamaEmbedder {
	return newOllamaEmbedder(baseURL, model, nomicQueryPrefix)
}

func newOllamaEmbedder(baseURL, model, prefix string) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL:    baseURL,
		model:      model,
		prefix:     prefix,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

type ollamaEmbedReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}
type ollamaEmbedResp struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns one vector per input text, in order, each prefixed with this
// embedder's task prefix. Ollama's endpoint embeds one prompt per call, so this
// loops (batching is a future optimization).
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	url := e.baseURL + "/api/embeddings"
	out := make([][]float32, len(texts))
	for i, t := range texts {
		var resp ollamaEmbedResp
		req := ollamaEmbedReq{Model: e.model, Prompt: e.prefix + t}
		if err := e.postJSON(ctx, url, req, &resp); err != nil {
			return nil, fmt.Errorf("ollama embed[%d]: %w", i, err)
		}
		if len(resp.Embedding) == 0 {
			return nil, fmt.Errorf("ollama embed[%d]: empty embedding", i)
		}
		out[i] = resp.Embedding
	}
	return out, nil
}

// postJSON marshals body, POSTs it, and decodes a 2xx JSON response into out.
// Non-2xx responses become errors that include the status code.
func (e *OllamaEmbedder) postJSON(ctx context.Context, url string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
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

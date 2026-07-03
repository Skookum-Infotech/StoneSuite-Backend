// Command rag-ingest-help ingests markdown docs into cp_rag_chunks (the
// control-plane app-help corpus), chunked by heading and embedded via the
// self-hosted Ollama nomic-embed-text model (ADR-001). Idempotent: each run
// replaces every chunk for a doc_key, so re-running never accumulates stale
// sections.
//
// Usage:
//
//	go run ./cmd/rag-ingest-help [docs-dir]
//
// docs-dir defaults to "docs". Reads CONTROL_PLANE_DB_URL, OLLAMA_BASE_URL,
// and AI_EMBED_MODEL from the environment / .env file (same as the server).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/config"
)

func main() {
	config.Load()
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	docsDir := "docs"
	if len(os.Args) > 1 {
		docsDir = os.Args[1]
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}
	defer pool.Close()

	embedder := ai.NewOllamaDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel)
	store := ai.NewCPHelpStore(pool)

	files, err := filepath.Glob(filepath.Join(docsDir, "*.md"))
	if err != nil {
		log.Fatalf("glob %s: %v", docsDir, err)
	}
	if len(files) == 0 {
		log.Printf("no markdown files found in %s -- nothing to ingest", docsDir)
		return
	}

	var failed int
	for _, path := range files {
		if err := ingestFile(ctx, embedder, store, path); err != nil {
			log.Printf("FAILED %s: %v", path, err)
			failed++
			continue
		}
		log.Printf("OK %s", path)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// ingestFile chunks one markdown file, embeds each section, and replaces its
// doc_key's chunks in cp_rag_chunks. A per-file failure is returned to the
// caller (which logs and continues with the remaining files).
func ingestFile(ctx context.Context, embedder ai.Embedder, store *ai.CPHelpStore, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	docKey := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	sections := ChunkMarkdown(string(raw), docKey)
	if len(sections) == 0 {
		return fmt.Errorf("no content to chunk")
	}

	chunks := make([]ai.HelpChunk, 0, len(sections))
	for _, sec := range sections {
		vecs, err := embedder.Embed(ctx, []string{sec.Content})
		if err != nil {
			return fmt.Errorf("embed section %q: %w", sec.Title, err)
		}
		chunks = append(chunks, ai.HelpChunk{Section: sec.Title, Content: sec.Content, Embedding: vecs[0]})
	}
	if err := store.ReplaceDoc(ctx, docKey, chunks); err != nil {
		return fmt.Errorf("replace doc: %w", err)
	}
	return nil
}

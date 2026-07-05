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
// With no argument, ingests the docs compiled into the binary at build time
// (stonesuite-backend/docs). Pass a directory to preview uncommitted doc
// edits before they're compiled in. Reads CONTROL_PLANE_DB_URL,
// OLLAMA_BASE_URL, and AI_EMBED_MODEL from the environment / .env file (same
// as the server).
//
// In production, prefer POST /api/platform/ai/reindex-help (platform-admin
// only) — it runs the same ai/helpdocs logic in-process on the already-
// deployed backend, with no SSH or local toolchain required.
package main

import (
	"context"
	"io/fs"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/helpdocs"
	"stonesuite-backend/config"
	"stonesuite-backend/docs"
)

func main() {
	config.Load()
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	var docsFS fs.FS = docs.FS
	if len(os.Args) > 1 {
		docsFS = os.DirFS(os.Args[1])
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}
	defer pool.Close()

	embedder := ai.NewOllamaDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel)
	store := ai.NewCPHelpStore(pool)

	res, err := helpdocs.IngestFS(ctx, embedder, store, docsFS)
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}
	for _, key := range res.Ingested {
		log.Printf("OK %s", key)
	}
	for key, msg := range res.Failed {
		log.Printf("FAILED %s: %s", key, msg)
	}
	if len(res.Failed) > 0 {
		os.Exit(1)
	}
}

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
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/ingest"
	"github.com/Skookum-Infotech/go-rag/provider/ollama"
	"stonesuite-backend/ai"
	"stonesuite-backend/config"
	"stonesuite-backend/controllers"
)

func main() {
	config.Load()
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}
	defer pool.Close()

	embedder := ollama.NewDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel, config.AppConfig.AIEmbedDim)

	var res ingest.Result
	var pruned int
	if len(os.Args) > 1 {
		// A directory argument previews uncommitted doc edits that aren't
		// compiled into docs.FS yet. It may be a subset of the real corpus,
		// so pruning (and recording cp_help_corpus_state, which describes
		// docs.FS specifically) would be wrong here — go through ingest
		// directly instead of the shared docs.FS-only helper.
		store := ai.NewCPHelpStore(pool, ai.EmbedFingerprint(embedder))
		res, err = ingest.IngestFS(ctx, embedder, store, os.DirFS(os.Args[1]), ingest.DefaultChunkOpts)
		if err != nil {
			log.Fatalf("ingest: %v", err)
		}
	} else {
		// No argument: ingest the docs compiled into this binary
		// (stonesuite-backend/docs), prune anything no longer in the
		// corpus, and record the sync in cp_help_corpus_state — the same
		// path POST /api/platform/ai/reindex-help and boot-time sync use.
		res, pruned, err = controllers.IngestHelpCorpus(ctx, pool, embedder)
		if err != nil {
			log.Fatalf("ingest: %v", err)
		}
		log.Printf("pruned %d chunks from docs no longer in the corpus", pruned)
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

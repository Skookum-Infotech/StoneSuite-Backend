package controllers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/ingest"
	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/docs"
)

// IngestHelpCorpus embeds every docs.FS file into cp_rag_chunks, prunes any
// chunk whose doc is no longer in the corpus, and records the resulting
// state (content hash + embedder fingerprint + timestamp) in
// cp_help_corpus_state so a later boot can tell whether a re-sync is
// needed. Shared by ReindexHelp (POST /api/platform/ai/reindex-help) and the
// boot-time help-corpus sync so the two ingestion paths never drift apart.
//
// A prune failure is logged and treated as non-fatal — it leaves stale
// chunks retrievable, which is preferable to failing the whole reindex over
// a delete that a later sync will retry.
func IngestHelpCorpus(ctx context.Context, cpPool *pgxpool.Pool, docEmbed ragcore.Embedder) (ingest.Result, int, error) {
	fingerprint := ai.EmbedFingerprint(docEmbed)
	store := ai.NewCPHelpStore(cpPool, fingerprint)

	res, err := ingest.IngestFS(ctx, docEmbed, store, docs.FS, ingest.DefaultChunkOpts)
	if err != nil {
		return ingest.Result{}, 0, fmt.Errorf("ingest help docs: %w", err)
	}

	// docs.FS is the complete corpus, so anything not in it is a removed doc.
	pruned, pruneErr := ingest.PruneMissing(ctx, store, docs.FS)
	if pruneErr != nil {
		slog.Error("help corpus prune failed", "err", pruneErr)
		pruned = 0
	}

	if !shouldRecordHelpState(len(res.Failed), pruneErr) {
		slog.Warn("help corpus state not recorded — will retry on next sync",
			"failed", len(res.Failed), "prune_err", pruneErr)
		return res, pruned, nil
	}

	hash, err := docs.ContentHash()
	if err != nil {
		return res, pruned, fmt.Errorf("compute help corpus content hash: %w", err)
	}
	if _, err := cpPool.Exec(ctx, `
		INSERT INTO cp_help_corpus_state (id, content_hash, embed_fingerprint, synced_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
			content_hash = EXCLUDED.content_hash,
			embed_fingerprint = EXCLUDED.embed_fingerprint,
			synced_at = EXCLUDED.synced_at`,
		hash, fingerprint); err != nil {
		return res, pruned, fmt.Errorf("record help corpus state: %w", err)
	}
	return res, pruned, nil
}

// shouldRecordHelpState reports whether cp_help_corpus_state should be
// updated after an ingest: only when every doc embedded successfully and the
// prune did not fail. Recording state after a partial failure would make the
// next boot believe the corpus is fully synced and skip retrying the docs
// that failed.
func shouldRecordHelpState(failed int, pruneErr error) bool {
	return failed == 0 && pruneErr == nil
}

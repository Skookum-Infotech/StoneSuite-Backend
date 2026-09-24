package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/provider/ollama"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/config"
	"stonesuite-backend/controllers"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/docs"
	"stonesuite-backend/metrics"
	"stonesuite-backend/tenancy"
)

// ragMaintenanceInterval is how often tenants are rescanned for new ones and
// each tenant's index is reconciled.
const ragMaintenanceInterval = 10 * time.Minute

// newChatClient builds the Ollama chat client every assistant path shares.
func newChatClient() *ollama.LLMClient {
	return ollama.NewLLMClient(config.AppConfig.OllamaBaseURL, config.AppConfig.AIChatModel).
		WithKeepAlive(config.AppConfig.AIKeepAlive)
}

func newDocEmbedder() *ollama.Embedder {
	return ollama.NewDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel, config.AppConfig.AIEmbedDim)
}

// startRAGIndexing starts one index-drain loop and one maintenance loop per
// servable tenant, then rescans the tenant list every ragMaintenanceInterval
// so a tenant provisioned after boot gets indexed without waiting for a
// restart. Exits when ctx is cancelled; per-tenant loops share ctx.
func startRAGIndexing(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router) {
	started := map[string]bool{}
	scan := func() {
		tenants, err := cp.ListTenants(ctx)
		if err != nil {
			slog.Error("rag-index: failed to list tenants", "err", err)
			return
		}
		for _, t := range tenants {
			if started[t.ID] || !t.Servable() {
				continue
			}
			pool, err := router.PoolFor(ctx, &t)
			if err != nil {
				slog.Error("rag-index: tenant pool error", "tenant", t.Slug, "err", err)
				continue
			}
			store := crmstore.For(t.DesignVersion)
			q := index.NewQueue(pool)
			w := index.NewWorker(q, crmstore.NewRAGRecordLoader(store, pool), newDocEmbedder(), ai.NewRagStore(pool))
			started[t.ID] = true
			go runTenantIndexWorker(ctx, t.Slug, w, q)
			go runTenantMaintenance(ctx, t.Slug, store, pool, q)
		}
	}

	scan()
	ticker := time.NewTicker(ragMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan()
		}
	}
}

// runTenantIndexWorker drains one tenant's rag_index_queue every 3s until ctx
// is cancelled, publishing queue-depth metrics on the same tick.
func runTenantIndexWorker(ctx context.Context, slug string, w *index.Worker, q *index.Queue) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.DrainOnce(ctx); err != nil {
				slog.Error("rag-index: drain error", "tenant", slug, "err", err)
			}
			if pending, age, err := q.Stats(ctx); err != nil {
				slog.Error("rag-index: stats error", "tenant", slug, "err", err)
			} else {
				metrics.SetRAGIndexQueueStats(slug, pending, age)
			}
		}
	}
}

// runTenantMaintenance runs index reconciliation and conversation retention
// immediately and then every ragMaintenanceInterval until ctx is cancelled.
func runTenantMaintenance(ctx context.Context, slug string, store crmstore.Store, pool *pgxpool.Pool, q *index.Queue) {
	maintain := func() {
		reconcileTenantIndex(ctx, slug, store, pool, q)
		pruneIdleConversations(ctx, slug, pool)
	}
	maintain()
	ticker := time.NewTicker(ragMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			maintain()
		}
	}
}

// indexedChunk is what reconciliation needs to know about one rag_chunks row.
type indexedChunk struct {
	updatedAt time.Time
	untyped   bool
}

// reconcileTenantIndex is the backstop for index-on-write (crmstore
// IndexingStore), which is best-effort. It enqueues:
//   - an upsert for every live record whose vector is missing, older than the
//     record, or has no record_type (indexed before the column existed — the
//     worker refreshes that with a scope-only update, no re-embed);
//   - a delete for every vector whose record no longer exists or is
//     soft-deleted — a lost delete job otherwise leaves the record answerable
//     forever.
//
// Deletes are only enqueued when every record list succeeded: a partial list
// would make live records look orphaned.
func reconcileTenantIndex(ctx context.Context, slug string, store crmstore.Store, pool *pgxpool.Pool, q *index.Queue) {
	rows, err := pool.Query(ctx, `SELECT source_id::text, updated_at, record_type IS NULL FROM rag_chunks`)
	if err != nil {
		slog.Error("rag-reconcile: read rag_chunks failed", "tenant", slug, "err", err)
		return
	}
	indexed := map[string]indexedChunk{}
	for rows.Next() {
		var id string
		var c indexedChunk
		if err := rows.Scan(&id, &c.updatedAt, &c.untyped); err != nil {
			rows.Close()
			slog.Error("rag-reconcile: scan failed", "tenant", slug, "err", err)
			return
		}
		indexed[id] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("rag-reconcile: rows error", "tenant", slug, "err", err)
		return
	}

	live := map[string]time.Time{}
	complete := true
	for _, key := range crmstore.CRMWorkflowKeys() {
		recs, err := store.ListRecords(ctx, pool, key, "all", "")
		if err != nil {
			slog.Error("rag-reconcile: list records failed", "tenant", slug, "key", key, "err", err)
			complete = false
			continue
		}
		for _, rec := range recs {
			live[rec.ID] = rec.UpdatedAt
		}
	}

	upserts, deletes := reconcilePlan(indexed, live, complete)
	for _, id := range upserts {
		if err := q.Enqueue(ctx, id, "upsert"); err != nil {
			slog.Error("rag-reconcile: enqueue failed", "tenant", slug, "source_id", id, "op", "upsert", "err", err)
		}
	}
	for _, id := range deletes {
		if err := q.Enqueue(ctx, id, "delete"); err != nil {
			slog.Error("rag-reconcile: enqueue failed", "tenant", slug, "source_id", id, "op", "delete", "err", err)
		}
	}
}

// reconcilePlan decides reconciliation's jobs from what is indexed and which
// records are live (id -> updated_at). listComplete false (some record list
// failed) suppresses every delete — a missing list would make live records
// look orphaned. Results are sorted for deterministic enqueue order.
func reconcilePlan(indexed map[string]indexedChunk, live map[string]time.Time, listComplete bool) (upserts, deletes []string) {
	for id, updatedAt := range live {
		if c, ok := indexed[id]; ok && !c.untyped && !updatedAt.After(c.updatedAt) {
			continue // vector current
		}
		upserts = append(upserts, id)
	}
	if listComplete {
		for id := range indexed {
			if _, ok := live[id]; !ok {
				deletes = append(deletes, id)
			}
		}
	}
	sort.Strings(upserts)
	sort.Strings(deletes)
	return upserts, deletes
}

// pruneIdleConversations applies AI_CONVERSATION_RETENTION_DAYS to one tenant.
func pruneIdleConversations(ctx context.Context, slug string, pool *pgxpool.Pool) {
	days := config.AppConfig.AIConversationRetentionDays
	if days <= 0 {
		return
	}
	n, err := ai.NewConversationStore(pool).DeleteIdle(ctx, time.Duration(days)*24*time.Hour)
	if err != nil {
		slog.Error("ai conversation retention failed", "tenant", slug, "err", err)
		return
	}
	if n > 0 {
		slog.Info("ai conversation retention", "tenant", slug, "deleted", n, "retention_days", days)
	}
}

// helpCorpusState is the (content hash, embedder fingerprint) pair compared
// to decide whether the help corpus needs re-ingesting — either the state
// last recorded in cp_help_corpus_state, or the values computed from the
// docs compiled into this binary and the embedder it's configured with.
type helpCorpusState struct {
	ContentHash      string
	EmbedFingerprint string
}

// needsHelpResync reports whether the help corpus must be re-ingested: true
// when no sync was ever recorded (stored == nil), or when the embedded doc
// content or the embedder that would produce new vectors has changed since
// the last recorded sync.
func needsHelpResync(stored *helpCorpusState, current helpCorpusState) bool {
	if stored == nil {
		return true
	}
	return stored.ContentHash != current.ContentHash || stored.EmbedFingerprint != current.EmbedFingerprint
}

// loadHelpCorpusState reads the single row cp_help_corpus_state tracks, or
// (nil, nil) if the help corpus has never been synced.
func loadHelpCorpusState(ctx context.Context, cpPool *pgxpool.Pool) (*helpCorpusState, error) {
	var s helpCorpusState
	err := cpPool.QueryRow(ctx, `SELECT content_hash, embed_fingerprint FROM cp_help_corpus_state WHERE id = 1`).
		Scan(&s.ContentHash, &s.EmbedFingerprint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load help corpus state: %w", err)
	}
	return &s, nil
}

// syncHelpCorpus re-ingests the app-help corpus at boot when the docs
// compiled into this binary or the configured embedder have changed since
// the last recorded sync (cp_help_corpus_state), so a doc edit or model
// swap reaches cp_rag_chunks without an operator remembering to call
// POST /api/platform/ai/reindex-help. Must run only after Ollama (or
// whatever embedder backend is configured) is reachable — callers are
// responsible for that ordering; see main.go's Ollama boot goroutine and its
// best-effort local-dev fallback.
func syncHelpCorpus(ctx context.Context, cpPool *pgxpool.Pool) {
	hash, err := docs.ContentHash()
	if err != nil {
		slog.Error("help corpus sync: content hash failed", "err", err)
		return
	}
	docEmbed := newDocEmbedder()
	current := helpCorpusState{ContentHash: hash, EmbedFingerprint: ai.EmbedFingerprint(docEmbed)}

	stored, err := loadHelpCorpusState(ctx, cpPool)
	if err != nil {
		slog.Error("help corpus sync: read state failed", "err", err)
		return
	}
	if !needsHelpResync(stored, current) {
		slog.Info("help corpus up to date")
		return
	}

	res, pruned, err := controllers.IngestHelpCorpus(ctx, cpPool, docEmbed)
	if err != nil {
		slog.Error("help corpus sync failed", "err", err)
		return
	}
	slog.Info("help corpus synced", "ingested", len(res.Ingested), "failed", len(res.Failed), "pruned", pruned)
}

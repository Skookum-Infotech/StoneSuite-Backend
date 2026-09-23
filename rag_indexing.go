package main

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/provider/ollama"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/config"
	"stonesuite-backend/crmstore"
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

// checkHelpCorpusFingerprint warns at boot when app-help vectors were made by
// a different embedder than the one now configured: those vectors are not
// comparable with query vectors, so help answers silently degrade to noise
// until POST /api/platform/ai/reindex-help (or rag-ingest-help) is run.
func checkHelpCorpusFingerprint(ctx context.Context, cpPool *pgxpool.Pool) {
	current := ai.EmbedFingerprint(newDocEmbedder())
	stale, err := ai.NewCPHelpStore(cpPool, current).StaleFingerprints(ctx, current)
	if err != nil {
		slog.Error("help corpus fingerprint check failed", "err", err)
		return
	}
	if len(stale) > 0 {
		slog.Warn("help corpus was embedded with a different model; reindex app help",
			"current", current, "stored", stale)
	}
}

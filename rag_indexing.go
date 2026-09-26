package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/provider/ollama"
	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/aisettings"
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

// ragDrainInterval is the normal cadence of the index-drain loop when the
// embedder is healthy.
const ragDrainInterval = 3 * time.Second

// ragDrainBackoffInitial and ragDrainBackoffMax bound the drain loop's
// exponential backoff once the embedder starts reporting rag.ErrUnavailable
// (e.g. Ollama is down or mid-restart): the first backoff wait is
// ragDrainBackoffInitial, doubling on each further consecutive failure, capped
// at ragDrainBackoffMax. Reset to ragDrainInterval on the next successful
// drain.
const (
	ragDrainBackoffInitial = 5 * time.Second
	ragDrainBackoffMax     = 2 * time.Minute
)

// ragDrainBackoff is the pure backoff calculation runTenantIndexWorker
// applies after consecutiveFailures consecutive rag.ErrUnavailable drains.
// Zero (or negative) means "healthy" — the normal ragDrainInterval cadence.
func ragDrainBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 0 {
		return ragDrainInterval
	}
	d := ragDrainBackoffInitial
	for i := 1; i < consecutiveFailures; i++ {
		d *= 2
		if d >= ragDrainBackoffMax {
			return ragDrainBackoffMax
		}
	}
	return d
}

// IndexCoordinator lets an external event — the platform AI toggle turning
// back on, or a tenant flipping its own switch on via PUT
// /api/tenant/ai/settings — nudge that tenant's maintenance loop to run its
// catch-up (revive errored jobs, then reconcile) immediately instead of
// waiting for the next ragMaintenanceInterval tick. One instance is shared
// for the whole process; startRAGIndexing registers a channel per tenant as
// it starts that tenant's loops.
type IndexCoordinator struct {
	mu     sync.Mutex
	nudges map[string]chan struct{}
}

// NewIndexCoordinator builds an empty IndexCoordinator.
func NewIndexCoordinator() *IndexCoordinator {
	return &IndexCoordinator{nudges: make(map[string]chan struct{})}
}

// register returns tenantID's nudge channel, creating it (buffered so a
// nudge sent before the maintenance loop is ready to receive is not lost)
// the first time it's asked for.
func (c *IndexCoordinator) register(tenantID string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.nudges[tenantID]
	if !ok {
		ch = make(chan struct{}, 1)
		c.nudges[tenantID] = ch
	}
	return ch
}

// CatchUp nudges one tenant's maintenance loop to catch up right now. A
// no-op for a tenant that has not been registered yet (not yet provisioned,
// or RAG indexing disabled) and non-blocking when a nudge is already pending
// — a second nudge before the first is consumed adds nothing.
func (c *IndexCoordinator) CatchUp(tenantID string) {
	c.mu.Lock()
	ch, ok := c.nudges[tenantID]
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// CatchUpAll nudges every registered tenant's maintenance loop — called after
// the platform-wide AI switch is turned back on, so every tenant's index
// catches up without waiting out ragMaintenanceInterval.
func (c *IndexCoordinator) CatchUpAll(ctx context.Context) {
	c.mu.Lock()
	ids := make([]string, 0, len(c.nudges))
	for id := range c.nudges {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	slog.InfoContext(ctx, "rag-index: platform re-enabled, nudging all tenants to catch up", "tenants", len(ids))
	for _, id := range ids {
		c.CatchUp(id)
	}
}

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
// aiCache and coordinator are each a single shared instance (not one per
// tenant, not package-level globals) so every tenant's loop sees the same
// 30s-TTL toggle cache and the same catch-up nudge registry the platform and
// tenant AI-settings handlers can also reach.
func startRAGIndexing(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router, aiCache *aisettings.Cache, coordinator *IndexCoordinator) {
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
			nudge := coordinator.register(t.ID)
			go runTenantIndexWorker(ctx, t.Slug, t.ID, w, q, aiCache, cp.Pool(), pool)
			go runTenantMaintenance(ctx, t.Slug, t.ID, store, cp.Pool(), pool, q, aiCache, nudge)
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

// runTenantIndexWorker drains one tenant's rag_index_queue on a timer until
// ctx is cancelled, publishing queue-depth metrics after every drain attempt.
// The timer runs at ragDrainInterval while the embedder is healthy and while
// AI is unavailable for this tenant (platform or tenant switch off — the
// drain is simply skipped, not backed off, so it resumes at full cadence the
// moment either switch flips back on); it backs off exponentially
// (ragDrainBackoff) across consecutive rag.ErrUnavailable failures and resets
// to ragDrainInterval on the next successful drain.
func runTenantIndexWorker(ctx context.Context, slug, tenantID string, w *index.Worker, q *index.Queue, aiCache *aisettings.Cache, cpPool, pool *pgxpool.Pool) {
	consecutiveFailures := 0
	timer := time.NewTimer(ragDrainInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			status, err := aiCache.Status(ctx, cpPool, pool, tenantID)
			if err != nil {
				slog.Error("rag-index: ai status check failed", "tenant", slug, "err", err)
			} else if !status.Available {
				timer.Reset(ragDrainInterval)
				continue
			}

			_, drainErr := w.DrainOnce(ctx)
			switch {
			case drainErr != nil && errors.Is(drainErr, ragcore.ErrUnavailable):
				consecutiveFailures++
				slog.Warn("rag-index: embedder unavailable, backing off", "tenant", slug, "consecutive_failures", consecutiveFailures)
			case drainErr != nil:
				slog.Error("rag-index: drain error", "tenant", slug, "err", drainErr)
				consecutiveFailures = 0
			default:
				consecutiveFailures = 0
			}

			if pending, age, statErr := q.Stats(ctx); statErr != nil {
				slog.Error("rag-index: stats error", "tenant", slug, "err", statErr)
			} else {
				metrics.SetRAGIndexQueueStats(slug, pending, age)
			}
			timer.Reset(ragDrainBackoff(consecutiveFailures))
		}
	}
}

// maintenanceAction is what a maintenance tick should do for one tenant,
// decided purely from current vs. previous availability by
// decideMaintenanceAction.
type maintenanceAction int

const (
	maintenanceSkip maintenanceAction = iota
	maintenanceCatchUp
	maintenanceReconcile
)

// decideMaintenanceAction is the pure gating + edge-detection rule
// runTenantMaintenance applies on every tick and nudge: unavailable always
// skips reconciliation entirely (conversation retention still runs — that's
// the caller's job, not this decision's); becoming available for the first
// time (prevAvailable false) always catches up (revive errored jobs, then
// reconcile); staying available just reconciles.
func decideMaintenanceAction(available, prevAvailable bool) maintenanceAction {
	switch {
	case !available:
		return maintenanceSkip
	case !prevAvailable:
		return maintenanceCatchUp
	default:
		return maintenanceReconcile
	}
}

// runTenantMaintenance runs index reconciliation (gated on AI availability)
// and conversation retention (never gated) immediately and then every
// ragMaintenanceInterval until ctx is cancelled. nudge — sent by
// IndexCoordinator.CatchUp/CatchUpAll, or the tenant settings PUT handler —
// forces an immediate catch-up (revive errored jobs, then reconcile) outside
// the normal tick, the way re-enabling AI for this tenant should not have to
// wait out ragMaintenanceInterval.
func runTenantMaintenance(ctx context.Context, slug, tenantID string, store crmstore.Store, cpPool, pool *pgxpool.Pool, q *index.Queue, aiCache *aisettings.Cache, nudge <-chan struct{}) {
	reclaimStuck := func() {
		if n, err := q.ReclaimStuck(ctx); err != nil {
			slog.Error("rag-index: reclaim stuck jobs failed", "tenant", slug, "err", err)
		} else if n > 0 {
			slog.Info("rag-index: reclaimed stuck inflight jobs", "tenant", slug, "reclaimed", n)
		}
	}
	purgeDone := func() {
		if n, err := q.PurgeDone(ctx); err != nil {
			slog.Error("rag-index: purge done jobs failed", "tenant", slug, "err", err)
		} else if n > 0 {
			slog.Info("rag-index: purged old done jobs", "tenant", slug, "purged", n)
		}
	}
	catchUp := func() {
		if n, err := q.Revive(ctx); err != nil {
			slog.Error("rag-index: revive failed", "tenant", slug, "err", err)
		} else if n > 0 {
			slog.Info("rag-index: revived errored jobs", "tenant", slug, "revived", n)
		}
		reconcileTenantIndex(ctx, slug, store, pool, q)
	}

	run := func(prevAvailable bool) bool {
		status, err := aiCache.Status(ctx, cpPool, pool, tenantID)
		if err != nil {
			slog.Error("rag-index: ai status check failed", "tenant", slug, "err", err)
			return prevAvailable
		}
		// A crashed worker can strand a job 'inflight' regardless of
		// whether AI is currently available, so this always runs.
		reclaimStuck()
		purgeDone()
		switch decideMaintenanceAction(status.Available, prevAvailable) {
		case maintenanceCatchUp:
			slog.Info("rag-index: assistant available, catching up", "tenant", slug)
			catchUp()
		case maintenanceReconcile:
			reconcileTenantIndex(ctx, slug, store, pool, q)
		case maintenanceSkip:
			// paused: AI is off for this tenant (platform or tenant switch).
		}
		return status.Available
	}

	// Boot: treat "was available" as true so a first-run available tenant
	// gets a plain reconcile, not a forced catch-up — matches the prior
	// behavior of unconditionally reconciling once at startup.
	available := run(true)
	pruneIdleConversations(ctx, slug, pool)

	ticker := time.NewTicker(ragMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			available = run(available)
			pruneIdleConversations(ctx, slug, pool)
		case <-nudge:
			// Force a catch-up regardless of the previous state — an
			// explicit nudge (platform re-enabled, or this tenant's own
			// switch flipped on) should always revive+reconcile, not just
			// on a genuine edge.
			available = run(false)
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

// syncHelpCorpus re-ingests the app-help corpus when the docs compiled into
// this binary or the configured embedder have changed since the last recorded
// sync (cp_help_corpus_state), so a doc edit or model swap reaches
// cp_rag_chunks without an operator remembering to call
// POST /api/platform/ai/reindex-help. It returns nil when the corpus was synced
// or is already up to date, and an error otherwise (including when some docs
// failed to embed) so the caller can retry — see helpSyncer.
func syncHelpCorpus(ctx context.Context, cpPool *pgxpool.Pool) error {
	hash, err := docs.ContentHash()
	if err != nil {
		return fmt.Errorf("help corpus sync: content hash: %w", err)
	}
	docEmbed := newDocEmbedder()
	current := helpCorpusState{ContentHash: hash, EmbedFingerprint: ai.EmbedFingerprint(docEmbed)}

	stored, err := loadHelpCorpusState(ctx, cpPool)
	if err != nil {
		return fmt.Errorf("help corpus sync: read state: %w", err)
	}
	if !needsHelpResync(stored, current) {
		slog.Info("help corpus up to date")
		return nil
	}

	res, pruned, err := controllers.IngestHelpCorpus(ctx, cpPool, docEmbed)
	if err != nil {
		return fmt.Errorf("help corpus sync: %w", err)
	}
	slog.Info("help corpus synced", "ingested", len(res.Ingested), "failed", len(res.Failed), "pruned", pruned)
	if len(res.Failed) > 0 {
		return fmt.Errorf("help corpus sync: %d doc(s) failed to embed", len(res.Failed))
	}
	return nil
}

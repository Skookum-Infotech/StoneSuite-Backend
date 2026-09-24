package index

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Skookum-Infotech/go-rag/chunk"
	"github.com/Skookum-Infotech/go-rag/rag"
)

// recordTokenBudget mirrors the ~512-token embedder context window
// (docs/ai-assistant.md) with headroom for the doc-embedding task prefix.
// rag_chunks stores one row per source_id (no multi-chunk-per-record split,
// unlike help docs — see ingest.DefaultChunkOpts), so text past this budget
// is silently truncated by the embedder rather than split. Custom fields are
// capped at 15 per workflow (workflow.MaxCustomFields), which keeps a
// typical record's rendered text well under this in practice; this constant
// exists to make an exception to that visible in logs rather than a schema
// change made speculatively before real records are observed exceeding it.
const recordTokenBudget = 400

// RecordLoader loads a record's embeddable form + scope columns by id.
// Implemented over crmstore.Store.GetRecord (adapter in the wiring layer).
type RecordLoader interface {
	Load(ctx context.Context, sourceID string) (doc rag.RecordDoc, workflowID, ownerUserID, teamID string, err error)
}

// ChunkSink reads and writes rag_chunks rows. Implemented by ai.RagStore.
//
// Meta and UpdateScope exist so the worker can avoid re-embedding text that
// has not changed. Embedding is by far the most expensive thing this worker
// does — on a CPU-bound box it dominates a full reindex — and the reconciliation
// sweep re-enqueues records on a timer, so without a skip the same unchanged
// text is embedded over and over.
type ChunkSink interface {
	Meta(ctx context.Context, sourceID string) (rag.ChunkMeta, bool, error)
	Upsert(ctx context.Context, c rag.Chunk) error
	UpdateScope(ctx context.Context, c rag.Chunk) error
	Delete(ctx context.Context, sourceID string) error
}

// jobQueue is the subset of Queue the worker needs (lets tests fake it).
type jobQueue interface {
	ClaimPending(ctx context.Context, n int) ([]Job, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id string) error
	Release(ctx context.Context, id string) error
}

// Worker turns queued jobs into fresh vectors for ONE tenant.
type Worker struct {
	q      jobQueue
	loader RecordLoader
	emb    rag.Embedder // a document embedder (search_document: prefix)
	sink   ChunkSink
	batch  int
}

// NewWorker builds a tenant index worker.
func NewWorker(q jobQueue, loader RecordLoader, emb rag.Embedder, sink ChunkSink) *Worker {
	return &Worker{q: q, loader: loader, emb: emb, sink: sink, batch: 20}
}

// DrainOnce processes one batch of pending jobs and returns how many it
// handled. Per-job failures are isolated (logged + re-queued); they don't
// abort the batch — with one exception: rag.ErrUnavailable (the embedder
// itself is unreachable, e.g. Ollama is down or mid-restart) means every
// other job in the batch would fail the same way, so that job is released
// back to pending WITHOUT charging a retry attempt, the rest of the batch is
// left untouched for the next drain, and the error is returned so the caller
// (runTenantIndexWorker) can back off instead of hammering a dead embedder
// every few seconds.
func (w *Worker) DrainOnce(ctx context.Context) (int, error) {
	jobs, err := w.q.ClaimPending(ctx, w.batch)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, j := range jobs {
		if err := w.process(ctx, j); err != nil {
			if errors.Is(err, rag.ErrUnavailable) {
				slog.Warn("rag index job deferred: embedder unavailable", "id", j.ID, "source_id", j.SourceID, "err", err)
				if relErr := w.q.Release(ctx, j.ID); relErr != nil {
					slog.Error("rag index release failed", "id", j.ID, "err", relErr)
				}
				handled++
				return handled, err
			}
			slog.Warn("rag index job failed", "id", j.ID, "source_id", j.SourceID, "err", err)
			_ = w.q.Fail(ctx, j.ID)
			handled++
			continue
		}
		_ = w.q.Complete(ctx, j.ID)
		handled++
	}
	return handled, nil
}

func (w *Worker) process(ctx context.Context, j Job) error {
	if j.Op == "delete" {
		return w.sink.Delete(ctx, j.SourceID)
	}
	doc, wfID, owner, team, err := w.loader.Load(ctx, j.SourceID)
	if err != nil {
		return err
	}
	content := rag.RenderRecord(doc)
	if est := chunk.EstimateTokens(content); est > recordTokenBudget {
		slog.Warn("rag record exceeds embed token budget; embedder will truncate it silently",
			"source_id", j.SourceID, "estimated_tokens", est, "budget", recordTokenBudget)
	}
	next := rag.Chunk{
		SourceID: j.SourceID, WorkflowID: wfID, OwnerUserID: owner, TeamID: team,
		Kind:    doc.WorkflowKey,
		Content: content, ContentHash: rag.VectorHash(w.fingerprint(), content),
	}

	prev, indexed, err := w.sink.Meta(ctx, j.SourceID)
	if err != nil {
		return err
	}
	if indexed {
		done, err := w.reuseExisting(ctx, prev, next)
		if err != nil || done {
			return err
		}
	}
	return w.embedAndUpsert(ctx, next)
}

// fingerprint names the vector space this worker's embedder produces, so a
// model or task-prefix change invalidates every stored hash. An embedder that
// cannot describe itself yields "", which simply means model changes are not
// detectable — see rag.VectorHash.
func (w *Worker) fingerprint() string {
	if f, ok := w.emb.(rag.Fingerprinter); ok {
		return f.Fingerprint()
	}
	return ""
}

// reuseExisting decides whether an already-indexed record can avoid a re-embed.
// It reports done=true when it has fully handled the job.
//
// The hash covers the rendered text and the embedder that produced the stored
// vector (rag.VectorHash), so a matching hash means the stored embedding is
// still correct and there is nothing an embedder could add. That leaves exactly
// one thing that can be stale: the scope columns.
//
// Those are the subtle part. RenderRecord does not emit the owner — crmstore
// handles owner explicitly, outside the CoreFields registry — so reassigning a
// record leaves its text, and therefore its hash, byte-identical. Skipping on
// the hash alone would keep the OLD owner_user_id in rag_chunks: the previous
// owner would go on retrieving the record under "own" scope, and the new owner
// could not find their own record. Re-embedding identical text just to correct
// an id would equally be waste, so ownership changes take the UPDATE path.
//
// Owner reassignment is a first-class CRM operation here (the workflow engine
// has a dedicated assign_owner transition action), which is what makes that
// third branch worth its cost rather than a premature optimisation.
func (w *Worker) reuseExisting(ctx context.Context, prev rag.ChunkMeta, next rag.Chunk) (done bool, err error) {
	if prev.ContentHash != next.ContentHash {
		return false, nil // text or embedder changed — the stored vector is stale
	}
	// Up to date or not, the row is touched: UpdateScope also bumps
	// updated_at, which is what the reconciliation sweep compares against the
	// record's own updated_at. Skipping the write when nothing changed left a
	// record whose updated_at moved without changing its text (a no-op
	// transition, an approval) looking stale forever, so the sweep re-queued
	// it every cycle.
	return true, w.sink.UpdateScope(ctx, next)
}

// embedAndUpsert pays for an embedding and writes the full row.
func (w *Worker) embedAndUpsert(ctx context.Context, c rag.Chunk) error {
	vecs, err := w.emb.Embed(ctx, []string{c.Content})
	if err != nil {
		return err
	}
	c.Embedding = vecs[0]
	return w.sink.Upsert(ctx, c)
}

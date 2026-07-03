package index

import (
	"context"
	"log/slog"

	"stonesuite-backend/ai"
)

// RecordLoader loads a record's embeddable form + scope columns by id.
// Implemented over crmstore.Store.GetRecord (adapter in the wiring layer).
type RecordLoader interface {
	Load(ctx context.Context, sourceID string) (doc ai.RecordDoc, workflowID, ownerUserID, teamID string, err error)
}

// ChunkSink upserts/deletes a rag_chunks row. Implemented by ai.RagStore.
type ChunkSink interface {
	Upsert(ctx context.Context, c ai.Chunk) error
	Delete(ctx context.Context, sourceID string) error
}

// jobQueue is the subset of Queue the worker needs (lets tests fake it).
type jobQueue interface {
	ClaimPending(ctx context.Context, n int) ([]Job, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id string) error
}

// Compile-time proof ai.RagStore satisfies ChunkSink.
var _ ChunkSink = (*ai.RagStore)(nil)

// Worker turns queued jobs into fresh vectors for ONE tenant.
type Worker struct {
	q      jobQueue
	loader RecordLoader
	emb    ai.Embedder // a document embedder (search_document: prefix)
	sink   ChunkSink
	batch  int
}

// NewWorker builds a tenant index worker.
func NewWorker(q jobQueue, loader RecordLoader, emb ai.Embedder, sink ChunkSink) *Worker {
	return &Worker{q: q, loader: loader, emb: emb, sink: sink, batch: 20}
}

// DrainOnce processes one batch of pending jobs and returns how many it handled.
// Per-job failures are isolated (logged + re-queued); they don't abort the batch.
func (w *Worker) DrainOnce(ctx context.Context) (int, error) {
	jobs, err := w.q.ClaimPending(ctx, w.batch)
	if err != nil {
		return 0, err
	}
	for _, j := range jobs {
		if err := w.process(ctx, j); err != nil {
			slog.Warn("rag index job failed", "id", j.ID, "source_id", j.SourceID, "err", err)
			_ = w.q.Fail(ctx, j.ID)
			continue
		}
		_ = w.q.Complete(ctx, j.ID)
	}
	return len(jobs), nil
}

func (w *Worker) process(ctx context.Context, j Job) error {
	if j.Op == "delete" {
		return w.sink.Delete(ctx, j.SourceID)
	}
	doc, wfID, owner, team, err := w.loader.Load(ctx, j.SourceID)
	if err != nil {
		return err
	}
	content := ai.RenderRecord(doc)
	hash := ai.ContentHash(content)
	vecs, err := w.emb.Embed(ctx, []string{content})
	if err != nil {
		return err
	}
	return w.sink.Upsert(ctx, ai.Chunk{
		SourceID: j.SourceID, WorkflowID: wfID, OwnerUserID: owner, TeamID: team,
		Content: content, ContentHash: hash, Embedding: vecs[0],
	})
}

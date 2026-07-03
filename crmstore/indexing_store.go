package crmstore

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Enqueuer is the write-side of the RAG index queue (implemented by
// ai/index.Queue). Defined here (consumer side) to avoid a crmstore->ai
// import edge.
type Enqueuer interface {
	Enqueue(ctx context.Context, sourceID, op string) error
}

// IndexingStore decorates a Store: after each successful write it enqueues a
// RAG index job. Enqueue failures are logged, never surfaced — indexing is
// best-effort at the call site; the durable queue + reconciliation sweep are
// the correctness backstop, so a transient enqueue error must not fail a write.
type IndexingStore struct {
	Store
	enq Enqueuer
}

// NewIndexingStore wraps inner with index-on-write behavior.
func NewIndexingStore(inner Store, enq Enqueuer) *IndexingStore {
	return &IndexingStore{Store: inner, enq: enq}
}

func (s *IndexingStore) index(ctx context.Context, id, op string) {
	if id == "" {
		return
	}
	if err := s.enq.Enqueue(ctx, id, op); err != nil {
		slog.Warn("rag index enqueue failed", "source_id", id, "op", op, "err", err)
	}
}

// CreateRecord creates a record, then enqueues it for indexing.
func (s *IndexingStore) CreateRecord(ctx context.Context, pool *pgxpool.Pool, key string, in CreateInput) (*workflow.Record, error) {
	rec, err := s.Store.CreateRecord(ctx, pool, key, in)
	if err == nil && rec != nil {
		s.index(ctx, rec.ID, "upsert")
	}
	return rec, err
}

// UpdateRecord updates a record, then enqueues it for re-indexing.
func (s *IndexingStore) UpdateRecord(ctx context.Context, pool *pgxpool.Pool, id string, core, custom map[string]any) error {
	err := s.Store.UpdateRecord(ctx, pool, id, core, custom)
	if err == nil {
		s.index(ctx, id, "upsert")
	}
	return err
}

// TransitionRecord transitions a record, then enqueues it for re-indexing.
func (s *IndexingStore) TransitionRecord(ctx context.Context, pool *pgxpool.Pool, id, toStatusID, actorIdentityID string) (*workflow.Record, error) {
	rec, err := s.Store.TransitionRecord(ctx, pool, id, toStatusID, actorIdentityID)
	if err == nil {
		s.index(ctx, id, "upsert")
	}
	return rec, err
}

// DeleteRecord deletes a record, then enqueues its vector for removal.
func (s *IndexingStore) DeleteRecord(ctx context.Context, pool *pgxpool.Pool, id string) error {
	err := s.Store.DeleteRecord(ctx, pool, id)
	if err == nil {
		s.index(ctx, id, "delete")
	}
	return err
}

// ConvertRecord converts a record to the next stage, then enqueues both the
// new record and the (still-existing) source record for re-indexing.
func (s *IndexingStore) ConvertRecord(ctx context.Context, pool *pgxpool.Pool, id, targetKey string, core, custom map[string]any, actorIdentityID string) (*workflow.Record, string, error) {
	newRec, srcID, err := s.Store.ConvertRecord(ctx, pool, id, targetKey, core, custom, actorIdentityID)
	if err == nil {
		if newRec != nil {
			s.index(ctx, newRec.ID, "upsert")
		}
		s.index(ctx, srcID, "upsert")
	}
	return newRec, srcID, err
}

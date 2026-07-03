package index

import (
	"context"
	"errors"
	"testing"

	"stonesuite-backend/ai"
)

var errBoom = errors.New("boom")

func ctxW(t *testing.T) context.Context { t.Helper(); return context.Background() }

type fakeQueue struct {
	pending []Job
	claimed map[string]bool
	failed  map[string]bool
	done    map[string]bool
}

func (q *fakeQueue) ClaimPending(_ context.Context, n int) ([]Job, error) {
	if q.claimed == nil {
		q.claimed = map[string]bool{}
	}
	var out []Job
	for _, j := range q.pending {
		if len(out) >= n {
			break
		}
		out = append(out, j)
		q.claimed[j.ID] = true
	}
	return out, nil
}
func (q *fakeQueue) Complete(_ context.Context, id string) error {
	if q.done == nil {
		q.done = map[string]bool{}
	}
	q.done[id] = true
	return nil
}
func (q *fakeQueue) Fail(_ context.Context, id string) error {
	if q.failed == nil {
		q.failed = map[string]bool{}
	}
	q.failed[id] = true
	return nil
}

type fakeLoader struct {
	doc                          ai.RecordDoc
	workflowID, owner, team, err string
	loadErr                      error
}

func (l *fakeLoader) Load(_ context.Context, _ string) (ai.RecordDoc, string, string, string, error) {
	if l.loadErr != nil {
		return ai.RecordDoc{}, "", "", "", l.loadErr
	}
	return l.doc, l.workflowID, l.owner, l.team, nil
}

type fakeChunkSink struct {
	upserts []Chunk
	deletes []string
}

func (s *fakeChunkSink) Upsert(_ context.Context, c Chunk) error {
	s.upserts = append(s.upserts, c)
	return nil
}
func (s *fakeChunkSink) Delete(_ context.Context, sourceID string) error {
	s.deletes = append(s.deletes, sourceID)
	return nil
}

func TestWorkerDrainsAndEmbeds(t *testing.T) {
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	loader := &fakeLoader{doc: ai.RecordDoc{WorkflowKey: "lead", StateName: "New"}}
	emb := &ai.FakeEmbedder{Dim: 768}
	sink := &fakeChunkSink{}

	w := NewWorker(q, loader, emb, sink)
	n, err := w.DrainOnce(ctxW(t))
	if err != nil || n != 1 {
		t.Fatalf("DrainOnce = %d,%v want 1,nil", n, err)
	}
	if len(sink.upserts) != 1 || sink.upserts[0].SourceID != "rec-1" {
		t.Fatalf("expected 1 upsert for rec-1, got %+v", sink.upserts)
	}
	if !q.done["1"] {
		t.Fatal("successful job must be marked done")
	}
}

func TestWorkerReEnqueuesOnEmbedError(t *testing.T) {
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	emb := &ai.FakeEmbedder{Err: errBoom}
	w := NewWorker(q, &fakeLoader{}, emb, &fakeChunkSink{})
	if _, err := w.DrainOnce(ctxW(t)); err != nil {
		t.Fatal(err) // DrainOnce swallows per-job errors, returns count
	}
	if q.failed["1"] != true {
		t.Fatal("failed job must be returned to the queue for retry")
	}
}

func TestWorkerHandlesDeleteWithoutEmbedding(t *testing.T) {
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "delete"}}}
	emb := &ai.FakeEmbedder{Err: errBoom} // must not be called for deletes
	sink := &fakeChunkSink{}
	w := NewWorker(q, &fakeLoader{}, emb, sink)

	n, err := w.DrainOnce(ctxW(t))
	if err != nil || n != 1 {
		t.Fatalf("DrainOnce = %d,%v want 1,nil", n, err)
	}
	if len(sink.deletes) != 1 || sink.deletes[0] != "rec-1" {
		t.Fatalf("expected delete for rec-1, got %+v", sink.deletes)
	}
	if !q.done["1"] {
		t.Fatal("successful delete job must be marked done")
	}
}

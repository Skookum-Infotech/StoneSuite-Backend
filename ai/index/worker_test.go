package index

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Skookum-Infotech/go-rag/rag"
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
	doc                     rag.RecordDoc
	workflowID, owner, team string
	loadErr                 error
}

func (l *fakeLoader) Load(_ context.Context, _ string) (rag.RecordDoc, string, string, string, error) {
	if l.loadErr != nil {
		return rag.RecordDoc{}, "", "", "", l.loadErr
	}
	return l.doc, l.workflowID, l.owner, l.team, nil
}

// fakeChunkSink records writes and can pretend a record is already indexed
// (stored/storedFound) so the skip logic has something to compare against.
type fakeChunkSink struct {
	stored      rag.ChunkMeta
	storedFound bool

	upserts      []rag.Chunk
	scopeUpdates []rag.Chunk
	deletes      []string
}

func (s *fakeChunkSink) Meta(_ context.Context, _ string) (rag.ChunkMeta, bool, error) {
	return s.stored, s.storedFound, nil
}
func (s *fakeChunkSink) Upsert(_ context.Context, c rag.Chunk) error {
	s.upserts = append(s.upserts, c)
	return nil
}
func (s *fakeChunkSink) UpdateScope(_ context.Context, c rag.Chunk) error {
	s.scopeUpdates = append(s.scopeUpdates, c)
	return nil
}
func (s *fakeChunkSink) Delete(_ context.Context, sourceID string) error {
	s.deletes = append(s.deletes, sourceID)
	return nil
}

// scopeAfter reports the scope columns rag_chunks would hold after this drain,
// whichever route the worker took to get there — a full Upsert or a cheap
// UpdateScope. Tests assert on this rather than on which method was called, so
// they pin the property that matters (stale scope must never survive) without
// dictating the implementation shape.
func (s *fakeChunkSink) scopeAfter() (owner, team, workflow string, wrote bool) {
	if n := len(s.upserts); n > 0 {
		c := s.upserts[n-1]
		return c.OwnerUserID, c.TeamID, c.WorkflowID, true
	}
	if n := len(s.scopeUpdates); n > 0 {
		c := s.scopeUpdates[n-1]
		return c.OwnerUserID, c.TeamID, c.WorkflowID, true
	}
	return s.stored.OwnerUserID, s.stored.TeamID, s.stored.WorkflowID, false
}

// countingEmbedder fails the test if it is called more than it should be —
// the only reliable way to prove a re-embed was actually skipped. fp lets a
// test stand in for a different embedding model; empty means "indistinguishable
// vector space", which is what the plain fakes use.
type countingEmbedder struct {
	rag.FakeEmbedder
	calls int
	fp    string
}

func (e *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	return e.FakeEmbedder.Embed(ctx, texts)
}
func (e *countingEmbedder) Fingerprint() string { return e.fp }

func TestWorkerDrainsAndEmbeds(t *testing.T) {
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	loader := &fakeLoader{doc: rag.RecordDoc{WorkflowKey: "lead", StateName: "New"}}
	emb := &rag.FakeEmbedder{Dim: 768}
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

// TestWorkerStillEmbedsRecordsPastTheTokenBudget proves the token-budget
// check (worker.go's recordTokenBudget) is observational only — it logs, it
// never withholds the record. Records aren't split into multiple chunks
// (rag_chunks is one row per source_id), so a record that's too big to log
// about must still be indexed as-is; the alternative (dropping it) would be
// worse than the truncation the log line exists to surface.
func TestWorkerStillEmbedsRecordsPastTheTokenBudget(t *testing.T) {
	big := map[string]any{}
	for i := 0; i < 15; i++ {
		big[fmt.Sprintf("field%d", i)] = "this custom field value is deliberately long and repeated many times over to blow well past the four hundred token budget for a single embedded record chunk"
	}
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	loader := &fakeLoader{doc: rag.RecordDoc{WorkflowKey: "lead", StateName: "New", Custom: big}}
	emb := &rag.FakeEmbedder{Dim: 768}
	sink := &fakeChunkSink{}

	w := NewWorker(q, loader, emb, sink)
	n, err := w.DrainOnce(ctxW(t))
	if err != nil || n != 1 {
		t.Fatalf("DrainOnce = %d,%v want 1,nil", n, err)
	}
	if len(sink.upserts) != 1 {
		t.Fatalf("want the oversized record still upserted whole, got %+v", sink.upserts)
	}
}

// indexedWorker wires a worker whose store already holds a chunk for rec-1 with
// the given hash and scope, so the skip logic has a prior state to compare to.
func indexedWorker(t *testing.T, doc rag.RecordDoc, storedHash, storedOwner string, nowOwner string) (*Worker, *fakeChunkSink, *countingEmbedder) {
	t.Helper()
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	loader := &fakeLoader{doc: doc, owner: nowOwner}
	emb := &countingEmbedder{FakeEmbedder: rag.FakeEmbedder{Dim: 768}}
	sink := &fakeChunkSink{
		storedFound: true,
		stored:      rag.ChunkMeta{ContentHash: storedHash, OwnerUserID: storedOwner},
	}
	return NewWorker(q, loader, emb, sink), sink, emb
}

// TestWorkerSkipsEmbedWhenNothingChanged is the whole point of storing a
// content hash. The reconciliation sweep re-enqueues records on a timer, so
// without this every sweep pays to re-embed text that is byte-identical.
func TestWorkerSkipsEmbedWhenNothingChanged(t *testing.T) {
	doc := rag.RecordDoc{WorkflowKey: "lead", StateName: "New"}
	hash := rag.VectorHash("", rag.RenderRecord(doc))

	w, sink, emb := indexedWorker(t, doc, hash, "user-1", "user-1")
	if _, err := w.DrainOnce(ctxW(t)); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 0 {
		t.Errorf("unchanged record was embedded %d time(s); the stored hash should have skipped it", emb.calls)
	}
	if len(sink.upserts) != 0 {
		t.Errorf("unchanged record should not be rewritten, got %d upsert(s)", len(sink.upserts))
	}
}

// TestWorkerRefreshesScopeWhenOwnerChanges is the security case.
//
// RenderRecord does not emit the owner — crmstore handles owner explicitly,
// outside the CoreFields registry — so reassigning a record leaves its content
// hash byte-identical. A skip that looks only at the hash would leave the OLD
// owner_user_id in rag_chunks: the previous owner keeps retrieving the record
// under "own" scope, and the new owner cannot find their own record.
//
// This asserts the outcome, not the route: correcting it with a full Upsert or
// with a cheap UpdateScope both pass. Leaving it stale does not.
func TestWorkerRefreshesScopeWhenOwnerChanges(t *testing.T) {
	doc := rag.RecordDoc{WorkflowKey: "lead", StateName: "New"}
	hash := rag.VectorHash("", rag.RenderRecord(doc))

	w, sink, _ := indexedWorker(t, doc, hash, "old-owner", "new-owner")
	if _, err := w.DrainOnce(ctxW(t)); err != nil {
		t.Fatal(err)
	}

	owner, _, _, wrote := sink.scopeAfter()
	if !wrote {
		t.Fatal("reassignment must write something; the worker skipped entirely and left the old owner in place")
	}
	if owner != "new-owner" {
		t.Fatalf("rag_chunks.owner_user_id = %q, want %q — the old owner can still retrieve this record under \"own\" scope", owner, "new-owner")
	}
}

// TestWorkerReEmbedsWhenContentChanges guards the other direction: the skip
// must not be so eager that genuinely edited records keep a stale vector.
func TestWorkerReEmbedsWhenContentChanges(t *testing.T) {
	doc := rag.RecordDoc{WorkflowKey: "lead", StateName: "Qualified"}

	w, sink, emb := indexedWorker(t, doc, rag.VectorHash("", "something entirely different"), "user-1", "user-1")
	if _, err := w.DrainOnce(ctxW(t)); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 1 {
		t.Errorf("changed record embedded %d time(s), want exactly 1", emb.calls)
	}
	if len(sink.upserts) != 1 {
		t.Fatalf("changed record must be upserted, got %d upsert(s)", len(sink.upserts))
	}
	if got := sink.upserts[0].ContentHash; got != rag.VectorHash("", rag.RenderRecord(doc)) {
		t.Errorf("upsert stored hash %q, want the freshly-rendered content's hash", got)
	}
}

// TestWorkerReEmbedsWhenEmbedderChanges is the case the fingerprint exists to
// defuse, and it is the one a content-only hash gets silently wrong.
//
// Swapping the embedding model — or merely correcting its task prefix, as
// Phase 0 does — changes the vector space while leaving every record's text
// byte-identical. Hash the content alone and the reindex that is supposed to
// rebuild the index would report "unchanged" for everything and skip all of
// it, leaving vectors in place that incoming queries can no longer match. The
// fix meant to repair retrieval would be neutralised by the optimisation meant
// to save money, with no error anywhere.
func TestWorkerReEmbedsWhenEmbedderChanges(t *testing.T) {
	doc := rag.RecordDoc{WorkflowKey: "lead", StateName: "New"}

	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	loader := &fakeLoader{doc: doc, owner: "user-1"}
	// The stored row was embedded by the previous model; the worker now runs a
	// different one. Text and scope are otherwise identical.
	emb := &countingEmbedder{FakeEmbedder: rag.FakeEmbedder{Dim: 768}, fp: "arctic-embed\x00"}
	sink := &fakeChunkSink{
		storedFound: true,
		stored: rag.ChunkMeta{
			ContentHash: rag.VectorHash("nomic-embed-text\x00search_document: ", rag.RenderRecord(doc)),
			OwnerUserID: "user-1",
		},
	}

	w := NewWorker(q, loader, emb, sink)
	if _, err := w.DrainOnce(ctxW(t)); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 1 {
		t.Fatalf("embedder changed but the record was embedded %d time(s), want 1 — a reindex after a model swap must not skip", emb.calls)
	}
	if len(sink.upserts) != 1 {
		t.Fatalf("expected the record to be rewritten with a vector from the new model, got %d upsert(s)", len(sink.upserts))
	}
	if got := sink.upserts[0].ContentHash; got != rag.VectorHash(emb.fp, rag.RenderRecord(doc)) {
		t.Errorf("stored hash %q does not identify the new embedder; the next sweep would re-embed again", got)
	}
}

// TestWorkerEmbedsFirstTimeRecord covers the never-indexed path — there is no
// prior hash to compare, so it must embed.
func TestWorkerEmbedsFirstTimeRecord(t *testing.T) {
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	emb := &countingEmbedder{FakeEmbedder: rag.FakeEmbedder{Dim: 768}}
	sink := &fakeChunkSink{storedFound: false}

	w := NewWorker(q, &fakeLoader{doc: rag.RecordDoc{WorkflowKey: "lead"}}, emb, sink)
	if _, err := w.DrainOnce(ctxW(t)); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 1 || len(sink.upserts) != 1 {
		t.Fatalf("first-time record: embeds=%d upserts=%d, want 1 and 1", emb.calls, len(sink.upserts))
	}
}

func TestWorkerReEnqueuesOnEmbedError(t *testing.T) {
	q := &fakeQueue{pending: []Job{{ID: "1", SourceID: "rec-1", Op: "upsert"}}}
	emb := &rag.FakeEmbedder{Err: errBoom}
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
	emb := &rag.FakeEmbedder{Err: errBoom} // must not be called for deletes
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

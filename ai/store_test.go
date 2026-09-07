//go:build dbtest

package ai

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/rag"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE rag_chunks`); err != nil {
		t.Fatalf("truncate rag_chunks: %v", err)
	}
	// CASCADE takes ai_messages with it (conversation_id FK).
	if _, err := pool.Exec(context.Background(), `TRUNCATE ai_conversations CASCADE`); err != nil {
		t.Fatalf("truncate ai_conversations: %v", err)
	}
	return pool
}

func ctxS(t *testing.T) context.Context { t.Helper(); return context.Background() }

// nonZeroVec returns a non-degenerate 768-dim vector. Cosine distance against
// an all-zero vector is undefined (NaN), and pgvector's HNSW index silently
// excludes NaN-distance rows from ORDER BY ... <=> ... LIMIT results — so an
// all-zero test fixture would make every retrieval assertion in this package
// falsely see zero rows once run against the real HNSW-indexed schema.
func nonZeroVec() []float32 {
	v := make([]float32, 768)
	for i := range v {
		v[i] = 0.1
	}
	return v
}

// Retrieval (ownership enforcement under real Postgres + pgvector + HNSW) now
// runs through ai.RecordsCorpus — see adapter_dbtest_test.go. RagStore is the
// ingestion write path only: what the index worker calls after rendering and
// embedding a record.

func TestRagStoreUpsertInsertsThenUpdates(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)

	const recID = "44444444-4444-4444-4444-444444444444"
	const wfID = "55555555-5555-5555-5555-555555555555"
	c := rag.Chunk{
		SourceID: recID, WorkflowID: wfID,
		Content: "Workflow: lead\nState: New\n", ContentHash: "hash1",
		Embedding: make([]float32, 768),
	}
	if err := s.Upsert(ctxS(t), c); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctxS(t), `SELECT count(*) FROM rag_chunks WHERE source_id=$1`, recID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count after first upsert = %d, want 1", count)
	}

	// Second upsert for the same source_id must update in place, not duplicate.
	c.ContentHash = "hash2"
	c.Content = "Workflow: lead\nState: Qualified\n"
	if err := s.Upsert(ctxS(t), c); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctxS(t), `SELECT count(*) FROM rag_chunks WHERE source_id=$1`, recID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count after second upsert = %d, want 1 (update in place)", count)
	}
	var hash string
	if err := pool.QueryRow(ctxS(t), `SELECT content_hash FROM rag_chunks WHERE source_id=$1`, recID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "hash2" {
		t.Fatalf("content_hash = %q, want hash2 (must reflect latest upsert)", hash)
	}
}

// TestRagStoreUpsertAllowsEmptyWorkflowID covers v2 relational-store records:
// crmstore/rag_loader.go maps a non-UUID WorkflowID (that store's fixed
// lead/prospect/customer type key) to empty string. workflow_id must accept
// that as SQL NULL rather than failing the insert with SQLSTATE 22P02.
func TestRagStoreUpsertAllowsEmptyWorkflowID(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)

	const recID = "66666666-6666-6666-6666-666666666666"
	c := rag.Chunk{
		SourceID: recID, WorkflowID: "",
		Content: "Workflow: lead\nState: New\n", ContentHash: "hash1",
		Embedding: make([]float32, 768),
	}
	if err := s.Upsert(ctxS(t), c); err != nil {
		t.Fatalf("upsert with empty WorkflowID must succeed: %v", err)
	}
	var workflowID *string
	if err := pool.QueryRow(ctxS(t), `SELECT workflow_id FROM rag_chunks WHERE source_id=$1`, recID).Scan(&workflowID); err != nil {
		t.Fatal(err)
	}
	if workflowID != nil {
		t.Fatalf("workflow_id = %v, want SQL NULL", *workflowID)
	}
}

func TestRagStoreDeleteRemovesRow(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)

	const recID = "66666666-6666-6666-6666-666666666666"
	c := rag.Chunk{SourceID: recID, WorkflowID: recID, Content: "x", ContentHash: "h", Embedding: make([]float32, 768)}
	if err := s.Upsert(ctxS(t), c); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctxS(t), recID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctxS(t), `SELECT count(*) FROM rag_chunks WHERE source_id=$1`, recID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count after delete = %d, want 0", count)
	}
}

func TestRagStoreDeleteNonexistentIsNoop(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	if err := s.Delete(ctxS(t), "77777777-7777-7777-7777-777777777777"); err != nil {
		t.Fatalf("deleting a nonexistent chunk must not error: %v", err)
	}
}

// TestRagStoreMetaRoundTripsScopeAndHash proves Meta reads back exactly what
// Upsert wrote — the index worker's re-embed-skip decision (ai/index/worker.go
// reuseExisting) is only as trustworthy as this round trip.
func TestRagStoreMetaRoundTripsScopeAndHash(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	ctx := ctxS(t)

	const recID = "88888888-8888-8888-8888-888888888888"
	const wfID = "99999999-9999-9999-9999-999999999999"
	const owner = "aaaaaaaa-0000-0000-0000-000000000001"
	const team = "bbbbbbbb-0000-0000-0000-000000000001"

	if err := s.Upsert(ctx, rag.Chunk{
		SourceID: recID, WorkflowID: wfID, OwnerUserID: owner, TeamID: team,
		Content: "x", ContentHash: "hash-abc", Embedding: nonZeroVec(),
	}); err != nil {
		t.Fatal(err)
	}

	meta, found, err := s.Meta(ctx, recID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Meta must report found=true for a row that was just upserted")
	}
	if meta.ContentHash != "hash-abc" || meta.OwnerUserID != owner || meta.TeamID != team || meta.WorkflowID != wfID {
		t.Fatalf("Meta = %+v, want hash/owner/team/workflow to round-trip exactly", meta)
	}
}

func TestRagStoreMetaNotFoundForUnindexedRecord(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)

	_, found, err := s.Meta(ctxS(t), "cccccccc-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("Meta must report found=false for a source_id that was never indexed")
	}
}

// TestRagStoreUpdateScopeLeavesContentUntouched is the DB-backed proof behind
// the reassignment path in ai/index/worker.go: correcting ownership must not
// perturb the stored content, hash, or embedding — only the scope columns.
func TestRagStoreUpdateScopeLeavesContentUntouched(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	ctx := ctxS(t)

	const recID = "dddddddd-0000-0000-0000-000000000001"
	const oldOwner = "eeeeeeee-0000-0000-0000-000000000001"
	const newOwner = "eeeeeeee-0000-0000-0000-000000000002"
	if err := s.Upsert(ctx, rag.Chunk{
		SourceID: recID, WorkflowID: recID, OwnerUserID: oldOwner,
		Content: "original content", ContentHash: "original-hash", Embedding: nonZeroVec(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateScope(ctx, rag.Chunk{SourceID: recID, OwnerUserID: newOwner}); err != nil {
		t.Fatal(err)
	}

	meta, _, err := s.Meta(ctx, recID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.OwnerUserID != newOwner {
		t.Fatalf("owner_user_id = %q, want %q", meta.OwnerUserID, newOwner)
	}
	if meta.ContentHash != "original-hash" {
		t.Fatalf("content_hash = %q, want it untouched by UpdateScope", meta.ContentHash)
	}
	var content string
	if err := pool.QueryRow(ctx, `SELECT content FROM rag_chunks WHERE source_id=$1`, recID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "original content" {
		t.Fatalf("content = %q, want it untouched by UpdateScope", content)
	}
}

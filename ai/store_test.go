//go:build dbtest

package ai

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
	return pool
}

func ctxS(t *testing.T) context.Context { t.Helper(); return context.Background() }

func TestRagStoreUpsertInsertsThenUpdates(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)

	const recID = "44444444-4444-4444-4444-444444444444"
	const wfID = "55555555-5555-5555-5555-555555555555"
	c := Chunk{
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

func TestRagStoreDeleteRemovesRow(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)

	const recID = "66666666-6666-6666-6666-666666666666"
	c := Chunk{SourceID: recID, WorkflowID: recID, Content: "x", ContentHash: "h", Embedding: make([]float32, 768)}
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

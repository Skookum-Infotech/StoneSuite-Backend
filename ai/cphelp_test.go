//go:build dbtest

package ai

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/ingest"
)

func newCPTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE cp_rag_chunks`); err != nil {
		t.Fatalf("truncate cp_rag_chunks: %v", err)
	}
	return pool
}

// Retrieval (Search/SearchLexical) is now go-rag's pgvector.Corpus and is
// covered there — both as SQL-shape unit tests and its own dbtest. CPHelpStore
// is the ingestion write path only: ReplaceDoc is what stays StoneSuite-side
// because HelpCorpus (the read side) is unscoped and generic, but choosing
// *when* to replace app-help content is this app's decision, not a library's.

func TestCPHelpStoreReplaceDocIsIdempotent(t *testing.T) {
	pool := newCPTestPool(t)
	s := NewCPHelpStore(pool)
	ctx := context.Background()

	err := s.ReplaceDoc(ctx, "onboarding", []ingest.DocChunk{
		{Section: "Intro", Content: "Welcome to StoneSuite.", Embedding: nonZeroVec()},
		{Section: "Step 1", Content: "Create a tenant.", Embedding: nonZeroVec()},
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cp_rag_chunks WHERE doc_key = $1`, "onboarding").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count after first ReplaceDoc = %d, want 2", count)
	}

	// Re-running with a smaller/changed section set must replace, not append.
	err = s.ReplaceDoc(ctx, "onboarding", []ingest.DocChunk{
		{Section: "Intro v2", Content: "Welcome to StoneSuite (updated).", Embedding: nonZeroVec()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cp_rag_chunks WHERE doc_key = $1`, "onboarding").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count after second ReplaceDoc = %d, want 1 (replaced, not appended)", count)
	}
	var section string
	if err := pool.QueryRow(ctx, `SELECT section FROM cp_rag_chunks WHERE doc_key = $1`, "onboarding").Scan(&section); err != nil {
		t.Fatal(err)
	}
	if section != "Intro v2" {
		t.Fatalf("section = %q, want Intro v2", section)
	}
}

func TestCPHelpStoreReplaceDocDoesNotTouchOtherDocs(t *testing.T) {
	pool := newCPTestPool(t)
	s := NewCPHelpStore(pool)
	ctx := context.Background()

	if err := s.ReplaceDoc(ctx, "doc-a", []ingest.DocChunk{{Section: "A", Content: "a", Embedding: nonZeroVec()}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDoc(ctx, "doc-b", []ingest.DocChunk{{Section: "B", Content: "b", Embedding: nonZeroVec()}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cp_rag_chunks WHERE doc_key = $1`, "doc-a").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("doc-a count = %d, want 1 (must survive doc-b's ReplaceDoc)", count)
	}
}

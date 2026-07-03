//go:build dbtest

package ai

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
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

func TestCPHelpStoreSearchLabelsResultsAsHelp(t *testing.T) {
	pool := newCPTestPool(t)
	s := NewCPHelpStore(pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx,
		`INSERT INTO cp_rag_chunks (doc_key, section, content, embedding) VALUES ($1, $2, $3, $4)`,
		"onboarding", "Getting Started", "To create a lead, go to CRM > Leads > New.", pgvector.NewVector(nonZeroVec()))
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Search(ctx, nonZeroVec(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].SourceType != "help" {
		t.Fatalf("SourceType = %q, want help", got[0].SourceType)
	}
	if got[0].SourceID != "Getting Started" {
		t.Fatalf("SourceID = %q, want the section label", got[0].SourceID)
	}
}

func TestCPHelpStoreReplaceDocIsIdempotent(t *testing.T) {
	pool := newCPTestPool(t)
	s := NewCPHelpStore(pool)
	ctx := context.Background()

	err := s.ReplaceDoc(ctx, "onboarding", []HelpChunk{
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
	err = s.ReplaceDoc(ctx, "onboarding", []HelpChunk{
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

	if err := s.ReplaceDoc(ctx, "doc-a", []HelpChunk{{Section: "A", Content: "a", Embedding: nonZeroVec()}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDoc(ctx, "doc-b", []HelpChunk{{Section: "B", Content: "b", Embedding: nonZeroVec()}}); err != nil {
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

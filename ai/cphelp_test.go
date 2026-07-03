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

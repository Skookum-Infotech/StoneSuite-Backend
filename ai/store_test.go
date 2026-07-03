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

// TestRagStoreSearchScopedEnforcesOwnership is the DB-backed proof behind the
// inviolable buildScopedSearch tests: a real caller with scope=own must never
// retrieve another user's chunk, even though it's in the same tenant DB.
func TestRagStoreSearchScopedEnforcesOwnership(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	ctx := ctxS(t)

	const userA = "aaaaaaaa-0000-0000-0000-000000000001"
	const userB = "aaaaaaaa-0000-0000-0000-000000000002"
	const teamX = "bbbbbbbb-0000-0000-0000-000000000001"

	mustUpsert := func(sourceID, owner, team, content string) {
		t.Helper()
		if err := s.Upsert(ctx, Chunk{
			SourceID: sourceID, WorkflowID: sourceID, OwnerUserID: owner, TeamID: team,
			Content: content, ContentHash: content, Embedding: make([]float32, 768),
		}); err != nil {
			t.Fatalf("upsert %s: %v", sourceID, err)
		}
	}
	mustUpsert("10000000-0000-0000-0000-000000000001", userA, teamX, "owned by A, in team X")
	mustUpsert("10000000-0000-0000-0000-000000000002", userB, teamX, "owned by B, in team X")
	mustUpsert("10000000-0000-0000-0000-000000000003", userB, "", "owned by B, no team")

	qv := make([]float32, 768)

	// own: A sees only A's chunk.
	got, err := s.SearchScoped(ctx, qv, "own", userA, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "10000000-0000-0000-0000-000000000001" {
		t.Fatalf("own scope for A = %+v, want exactly A's chunk", got)
	}

	// team: A (in team X) sees A's + B's team-X chunk, but not B's teamless one.
	got, err = s.SearchScoped(ctx, qv, "team", userA, []string{teamX}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("team scope for A = %+v, want 2 (A's own + B's team-X chunk)", got)
	}
	for _, c := range got {
		if c.SourceID == "10000000-0000-0000-0000-000000000003" {
			t.Fatalf("team scope leaked B's teamless chunk: %+v", got)
		}
	}

	// all: sees everything regardless of owner/team.
	got, err = s.SearchScoped(ctx, qv, "all", userA, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("all scope = %d results, want 3", len(got))
	}

	// unknown/unset scope: fail closed, zero results (never falls through to all).
	got, err = s.SearchScoped(ctx, qv, "", userA, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown scope = %+v, want 0 (fail closed)", got)
	}
}

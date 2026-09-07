//go:build dbtest

package ai

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// TestRecordsCorpusEnforcesOwnership is the DB-backed proof behind the
// inviolable scope invariant — now exercised through the actual production
// wiring (ScopeFilter -> RecordsCorpus -> go-rag's pgvector.Corpus) rather
// than internal store methods, so a regression anywhere in that chain,
// including in the vendored library, is caught here. A real caller with
// scope=own must never retrieve another user's chunk, even though it's in the
// same tenant database and even under the real HNSW index.
func TestRecordsCorpusEnforcesOwnership(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	ctx := ctxS(t)

	const userA = "aaaaaaaa-0000-0000-0000-000000000001"
	const userB = "aaaaaaaa-0000-0000-0000-000000000002"
	const teamX = "bbbbbbbb-0000-0000-0000-000000000001"

	mustUpsert := func(sourceID, owner, team, content string) {
		t.Helper()
		if err := s.Upsert(ctx, rag.Chunk{
			SourceID: sourceID, WorkflowID: sourceID, OwnerUserID: owner, TeamID: team,
			Content: content, ContentHash: content, Embedding: nonZeroVec(),
		}); err != nil {
			t.Fatalf("upsert %s: %v", sourceID, err)
		}
	}
	mustUpsert("10000000-0000-0000-0000-000000000001", userA, teamX, "widget order owned by A, in team X")
	mustUpsert("10000000-0000-0000-0000-000000000002", userB, teamX, "widget order owned by B, in team X")
	mustUpsert("10000000-0000-0000-0000-000000000003", userB, "", "widget order owned by B, no team")

	qv := nonZeroVec()

	for _, tc := range []struct {
		name      string
		scope     string
		wantCount int
		wantOnly  string // non-empty: the single source_id expected when wantCount == 1
	}{
		{"own: A sees only A's chunk", "own", 1, "10000000-0000-0000-0000-000000000001"},
		{"retired team scope fails closed", "team", 0, ""},
		{"all: sees everything regardless of owner/team", "all", 3, ""},
		{"unknown/unset scope fails closed", "", 0, ""},
	} {
		t.Run(tc.name+" (vector)", func(t *testing.T) {
			corpus := RecordsCorpus(pool, tc.scope, userA)
			got, err := corpus.SearchVector(ctx, qv, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d results, want %d: %+v", len(got), tc.wantCount, got)
			}
			if tc.wantOnly != "" && got[0].SourceID != tc.wantOnly {
				t.Fatalf("SourceID = %q, want %q", got[0].SourceID, tc.wantOnly)
			}
		})

		// Both arms MUST share the identical scope clause, so a caller can
		// never see more via full-text search than via similarity search.
		t.Run(tc.name+" (lexical)", func(t *testing.T) {
			corpus := RecordsCorpus(pool, tc.scope, userA)
			got, err := corpus.SearchLexical(ctx, "widget", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d results, want %d: %+v", len(got), tc.wantCount, got)
			}
			if tc.wantOnly != "" && got[0].SourceID != tc.wantOnly {
				t.Fatalf("SourceID = %q, want %q", got[0].SourceID, tc.wantOnly)
			}
		})
	}
}

// TestHelpCorpusLabelsResultsAndIgnoresScope proves the adapter's wiring
// choice for HelpCorpus: SourceType is "help", SourceID is the section
// heading (not a row id — see the IDColumn: "section" option), and content is
// visible regardless of caller, since app-help is identical for every tenant.
func TestHelpCorpusLabelsResultsAndIgnoresScope(t *testing.T) {
	pool := newCPTestPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx,
		`INSERT INTO cp_rag_chunks (doc_key, section, content, embedding) VALUES ($1, $2, $3, $4)`,
		"onboarding", "Getting Started", "To create a lead, go to CRM > Leads > New.", pgvector.NewVector(nonZeroVec()))
	if err != nil {
		t.Fatal(err)
	}

	got, err := HelpCorpus(pool).SearchVector(ctx, nonZeroVec(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].SourceType != CorpusHelp {
		t.Fatalf("SourceType = %q, want %q", got[0].SourceType, CorpusHelp)
	}
	if got[0].SourceID != "Getting Started" {
		t.Fatalf("SourceID = %q, want the section label", got[0].SourceID)
	}
}

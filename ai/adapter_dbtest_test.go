//go:build dbtest

package ai

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// TestRecordsCorpusEnforcesPerTypeScope is the DB-backed proof behind the
// inviolable scope invariant, exercised through the production wiring
// (RecordScopeFilter -> RecordsCorpus -> go-rag's pgvector.Corpus) under the
// real HNSW index, on both retrieval arms:
//   - "own" never reaches another user's chunk;
//   - a type the caller holds no grant on is unreachable even when another
//     type is granted "all" — the cross-type leak the per-type scope fixed;
//   - a chunk with no record_type (indexed before the column existed) is
//     invisible until reindexed, never visible to everyone.
func TestRecordsCorpusEnforcesPerTypeScope(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	ctx := ctxS(t)

	const userA = "aaaaaaaa-0000-0000-0000-000000000001"
	const userB = "aaaaaaaa-0000-0000-0000-000000000002"
	const (
		leadA     = "10000000-0000-0000-0000-000000000001"
		leadB     = "10000000-0000-0000-0000-000000000002"
		customerB = "10000000-0000-0000-0000-000000000003"
		customerA = "10000000-0000-0000-0000-000000000004"
		untyped   = "10000000-0000-0000-0000-000000000005"
	)

	mustUpsert := func(sourceID, owner, kind, content string) {
		t.Helper()
		if err := s.Upsert(ctx, rag.Chunk{
			SourceID: sourceID, OwnerUserID: owner, Kind: kind,
			Content: content, ContentHash: content, Embedding: nonZeroVec(),
		}); err != nil {
			t.Fatalf("upsert %s: %v", sourceID, err)
		}
	}
	mustUpsert(leadA, userA, "lead", "widget lead owned by A")
	mustUpsert(leadB, userB, "lead", "widget lead owned by B")
	mustUpsert(customerB, userB, "customer", "widget customer owned by B")
	mustUpsert(customerA, userA, "customer", "widget customer owned by A")
	mustUpsert(untyped, userA, "", "widget record indexed before record_type existed")

	qv := nonZeroVec()

	for _, tc := range []struct {
		name   string
		grants Grants
		want   []string
	}{
		{"lead own: only A's lead", Grants{"lead": ScopeOwn}, []string{leadA}},
		{"lead all + no customer grant: every lead, no customers", Grants{"lead": ScopeAll}, []string{leadA, leadB}},
		{"lead all + customer own: every lead, only A's customer", Grants{"lead": ScopeAll, "customer": ScopeOwn}, []string{customerA, leadA, leadB}},
		{"everything all: every typed row, never the untyped one", Grants{"lead": ScopeAll, "prospect": ScopeAll, "customer": ScopeAll}, []string{customerA, customerB, leadA, leadB}},
		{"retired team scope fails closed", Grants{"lead": "team"}, nil},
		{"no grants fails closed", Grants{}, nil},
	} {
		check := func(t *testing.T, got []rag.Citation, err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, c := range got {
				ids = append(ids, c.SourceID)
			}
			want := append([]string(nil), tc.want...)
			sort.Strings(ids)
			sort.Strings(want)
			if strings.Join(ids, ",") != strings.Join(want, ",") {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
		}
		t.Run(tc.name+" (vector)", func(t *testing.T) {
			got, err := RecordsCorpus(pool, tc.grants, userA).SearchVector(ctx, qv, 10)
			check(t, got, err)
		})
		// Both arms MUST share the identical scope clause, so a caller can
		// never see more via full-text search than via similarity search.
		t.Run(tc.name+" (lexical)", func(t *testing.T) {
			got, err := RecordsCorpus(pool, tc.grants, userA).SearchLexical(ctx, "widget", 10)
			check(t, got, err)
		})
	}
}

// TestHelpCorpusLabelsResultsAndIgnoresScope proves the adapter's wiring
// choice for HelpCorpus: SourceType is "help", SourceID is "<doc> › <section>"
// (not a row id, and not the bare section — see helpIDColumn), and content is
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
	if got[0].SourceID != "onboarding › Getting Started" {
		t.Fatalf("SourceID = %q, want the doc-qualified section label", got[0].SourceID)
	}
}

//go:build dbtest

package ai

import (
	"context"
	"testing"

	"github.com/Skookum-Infotech/go-rag/eval"
	"github.com/Skookum-Infotech/go-rag/rag"
)

// fixtureVec returns a 768-dim vector with every element set to v, distinct
// from another fixtureVec(v') whenever v != v' — enough for pgvector cosine
// distance to rank them apart. Not a real embedding: this proves the eval
// package's Run/RecallAtK wiring reaches the real production retrieval path
// (ai.RecordsCorpus over live Postgres+pgvector), not retrieval quality
// against real semantic content.
func fixtureVec(v float32) []float32 {
	out := make([]float32, 768)
	for i := range out {
		out[i] = v
	}
	return out
}

// TestEvalHarnessWiresIntoRecordsCorpus proves eval.Run/eval.AssertMinRecall
// work end to end against the actual production retrieval path — ai.
// RecordsCorpus wrapping go-rag's pgvector.Corpus — not just against fakes.
//
// This is a mechanism test, not a quality gate: the fixture vectors are
// synthetic (see fixtureVec), so a perfect score here says the plumbing is
// correct, not that real retrieval is any good. A genuine quality gate needs
// a golden set built from real questions and real embeddings, which needs
// live usage data this environment doesn't have — see the go-rag Phase 2
// plan notes. That golden set is future work; this test is what it will run
// against.
func TestEvalHarnessWiresIntoRecordsCorpus(t *testing.T) {
	pool := newTestPool(t)
	s := NewRagStore(pool)
	ctx := ctxS(t)

	const userA = "aaaaaaaa-0000-0000-0000-000000000010"
	const nearID = "20000000-0000-0000-0000-000000000001"
	const farID = "20000000-0000-0000-0000-000000000002"

	near := fixtureVec(0.11)
	far := fixtureVec(0.9)
	for _, tc := range []struct {
		id  string
		vec []float32
	}{{nearID, near}, {farID, far}} {
		if err := s.Upsert(ctx, rag.Chunk{
			SourceID: tc.id, WorkflowID: tc.id, OwnerUserID: userA,
			Content: "fixture content for " + tc.id, ContentHash: tc.id,
			Embedding: tc.vec,
		}); err != nil {
			t.Fatalf("upsert %s: %v", tc.id, err)
		}
	}

	golden := []eval.RetrievalCase{
		{Question: "any question — the fixture query vector decides ranking", Relevant: []string{"record:" + nearID}},
	}
	retrieve := func(ctx context.Context, _ string) ([]rag.Citation, error) {
		return RecordsCorpus(pool, "own", userA).SearchVector(ctx, near, 5)
	}

	rep := eval.Run(ctx, golden, 5, retrieve)
	eval.AssertMinRecall(t, rep, 1.0)
}

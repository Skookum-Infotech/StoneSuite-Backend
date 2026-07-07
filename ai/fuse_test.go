package ai

import "testing"

// TestFuseRRF_SingleListPreservesOrderAndCapsAtLimit confirms the required
// invariant that makes this a safe drop-in beside vector-only retrieval: with
// a single non-empty list, ranks strictly increase so RRF scores strictly
// decrease, meaning fuseRRF returns that list in its original order.
func TestFuseRRF_SingleListPreservesOrderAndCapsAtLimit(t *testing.T) {
	list := []Citation{
		{SourceType: "record", SourceID: "a"},
		{SourceType: "record", SourceID: "b"},
		{SourceType: "record", SourceID: "c"},
	}
	got := fuseRRF(4, list, nil)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, c := range got {
		if c.SourceID != list[i].SourceID {
			t.Fatalf("order not preserved: got %+v, want %+v", got, list)
		}
	}

	capped := fuseRRF(2, list, nil)
	if len(capped) != 2 || capped[0].SourceID != "a" || capped[1].SourceID != "b" {
		t.Fatalf("capped = %+v, want [a b]", capped)
	}
}

// TestFuseRRF_DocInBothListsOutranksSingleList proves the whole point of RRF:
// a document ranked in BOTH the vector and lexical lists must score higher
// than one ranked only in one, even if that one ranked #1 there.
func TestFuseRRF_DocInBothListsOutranksSingleList(t *testing.T) {
	vector := []Citation{
		{SourceType: "record", SourceID: "vector-only"}, // rank 1 in vector
		{SourceType: "record", SourceID: "both"},        // rank 2 in vector
	}
	lexical := []Citation{
		{SourceType: "record", SourceID: "both"}, // rank 1 in lexical
	}
	got := fuseRRF(10, vector, lexical)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2, got %+v", len(got), got)
	}
	if got[0].SourceID != "both" {
		t.Fatalf("doc present in both lists must rank first, got %+v", got)
	}
}

// TestFuseRRF_DedupesBySourceTypeAndID confirms a citation appearing in both
// lists is merged into a single entry, not duplicated.
func TestFuseRRF_DedupesBySourceTypeAndID(t *testing.T) {
	a := []Citation{{SourceType: "record", SourceID: "x"}}
	b := []Citation{{SourceType: "record", SourceID: "x"}}
	got := fuseRRF(10, a, b)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (deduped), got %+v", len(got), got)
	}
}

// TestFuseRRF_DifferentSourceTypeSameIDNotDeduped confirms the dedup key is
// (SourceType, SourceID) together — a "record" and a "help" citation that
// happen to share an ID string must NOT collapse into one entry.
func TestFuseRRF_DifferentSourceTypeSameIDNotDeduped(t *testing.T) {
	a := []Citation{{SourceType: "record", SourceID: "same-id"}}
	b := []Citation{{SourceType: "help", SourceID: "same-id"}}
	got := fuseRRF(10, a, b)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (distinct source types), got %+v", len(got), got)
	}
}

// TestFuseRRF_EmptyAndNilListsHandled confirms nil/empty lists never panic
// and simply contribute nothing.
func TestFuseRRF_EmptyAndNilListsHandled(t *testing.T) {
	if got := fuseRRF(10, nil, nil); got == nil {
		// either nil or empty is acceptable; just must not panic and must have zero length
	} else if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}

	if got := fuseRRF(10, []Citation{}, []Citation{}); len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}

	only := []Citation{{SourceType: "record", SourceID: "a"}}
	got := fuseRRF(10, nil, only)
	if len(got) != 1 || got[0].SourceID != "a" {
		t.Fatalf("got %+v, want [a]", got)
	}
}

// TestFuseRRF_ZeroLimitReturnsAll confirms limit=0 means "no cap" rather than
// "return nothing" — callers always pass a real k, but the function itself
// must not silently truncate on a zero value.
func TestFuseRRF_ZeroLimitReturnsAll(t *testing.T) {
	list := []Citation{
		{SourceType: "record", SourceID: "a"},
		{SourceType: "record", SourceID: "b"},
		{SourceType: "record", SourceID: "c"},
	}
	got := fuseRRF(0, list, nil)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (limit=0 => no cap)", len(got))
	}
}

// TestFuseRRF_TiesBreakByFirstSeenOrder confirms deterministic output when
// two distinct documents end up with equal fused scores.
func TestFuseRRF_TiesBreakByFirstSeenOrder(t *testing.T) {
	// Both appear only in one list, at the same rank (both lists have exactly
	// one entry, so both get rank 0) -> equal scores. "first" must sort ahead
	// of "second" since it was seen first (first list processed first).
	first := []Citation{{SourceType: "record", SourceID: "first"}}
	second := []Citation{{SourceType: "record", SourceID: "second"}}
	got := fuseRRF(10, first, second)
	if len(got) != 2 || got[0].SourceID != "first" || got[1].SourceID != "second" {
		t.Fatalf("got %+v, want [first second] (tie broken by first-seen order)", got)
	}
}

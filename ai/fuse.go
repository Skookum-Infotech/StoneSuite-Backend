package ai

import "sort"

// rrfK is the Reciprocal Rank Fusion damping constant (standard default 60):
// score(d) = Σ 1/(rrfK + rank) over each ranked list d appears in (rank is
// 1-based). Larger rrfK flattens the contribution of top ranks.
const rrfK = 60

// fuseRRF merges ranked Citation lists by Reciprocal Rank Fusion, deduped by
// (SourceType, SourceID). A citation ranked highly in BOTH the vector and
// lexical lists rises above one ranked highly in only one. Returns up to limit
// citations, highest fused score first; a single non-empty list is returned in
// its original order (so vector-only behavior is unchanged). Ties break by
// first-seen order for determinism.
func fuseRRF(limit int, lists ...[]Citation) []Citation {
	type agg struct {
		c     Citation
		score float64
		order int
	}
	seen := map[string]*agg{}
	var ordered []*agg
	next := 0
	for _, list := range lists {
		for rank, c := range list {
			key := c.SourceType + "\x00" + c.SourceID
			a, ok := seen[key]
			if !ok {
				a = &agg{c: c, order: next}
				next++
				seen[key] = a
				ordered = append(ordered, a)
			}
			a.score += 1.0 / float64(rrfK+rank+1) // rank+1 => 1-based
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		return ordered[i].order < ordered[j].order
	})
	out := make([]Citation, 0, len(ordered))
	for _, a := range ordered {
		out = append(out, a.c)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

package rag

import "context"

// Corpus is one searchable body of text — a tenant's records, a help
// collection, a set of uploaded documents — exposing the two retrieval arms
// that fuseRRF combines: vector (semantic) and lexical (keyword/full-text).
//
// A Corpus is ALREADY SCOPED. It is constructed for a particular caller and
// returns only rows that caller may see, so no method takes an identity, a
// tenant, or a permission argument. That is deliberate and load-bearing: a
// scope passed per call is a scope somebody can forget to pass, or that an
// untrusted input can influence. Binding it at construction makes "search
// something you are not allowed to see" unrepresentable rather than merely
// discouraged. See ScopeFilter.
type Corpus interface {
	// Name identifies the corpus in citations (Citation.SourceType), log
	// lines, and error messages. Keep it short and stable — it is user-visible
	// via citations.
	Name() string

	// SearchVector returns up to k nearest neighbours of vec, ordered by
	// increasing distance, with Distance/DistanceValid populated.
	SearchVector(ctx context.Context, vec []float32, k int) ([]Citation, error)

	// SearchLexical returns up to k full-text matches for the raw query text.
	// Distance is left unset (DistanceValid false): a literal term match needs
	// no similarity floor to be considered relevant.
	SearchLexical(ctx context.Context, query string, k int) ([]Citation, error)
}

// CorpusConfig binds a corpus to its retrieval budget and relevance floor.
//
// Both are per-corpus rather than global because corpora differ in kind: a
// tenant's own records deserve more slots than a generic help collection, and
// what counts as "close enough" is not the same in a corpus of terse records
// as in one of prose.
type CorpusConfig struct {
	Corpus Corpus

	// K caps how many chunks this corpus contributes to one answer. Keep it
	// modest for small self-hosted models: every extra citation is more
	// prefill before generation starts, and the top matches are already the
	// most relevant ones.
	K int

	// FloorDistance is the maximum vector distance at which a match still
	// counts as genuine grounding. If NO citation from this corpus clears it,
	// the corpus contributes nothing to the answer at all — better an honest
	// refusal than grounding a weak model in noise it will hallucinate over.
	// Zero disables the floor for this corpus.
	FloorDistance float64

	// RerankK is how many candidates to retrieve and fuse before a configured
	// Reranker (see Orchestrator.WithReranker) cuts back down to K. Only takes
	// effect when the Orchestrator has a Reranker AND RerankK > K — otherwise
	// retrieval stays at K, matching pre-reranking behavior exactly. Retrieve
	// wide, rerank down: a reranker is far more accurate than RRF fusion at
	// picking the truly best K, but too slow to run over the whole corpus, so
	// RRF does the cheap wide cut and the reranker does the precise narrow one.
	RerankK int
}

// ScopeFilter produces the SQL boolean expression, and its arguments, that
// narrow a corpus to the rows a caller is permitted to see. firstArg is the
// lowest placeholder number not already taken by the query itself, so a filter
// numbers its own parameters from there ($2, $3, ...).
//
// A filter may only ever NARROW. Returning a clause that widens the result set
// is a security bug, not a feature — the same invariant the record filter
// engine holds.
//
// Never interpolate a caller-supplied value into the clause string; return it
// in args and reference it by placeholder.
type ScopeFilter func(firstArg int) (clause string, args []any)

// DenyAll matches no rows. It is the fail-closed default: a corpus built
// without a filter, or from an unrecognised scope, returns nothing rather than
// everything. Prefer returning this explicitly from a scope resolver's default
// branch over returning nil, so the intent is visible at the call site.
func DenyAll(int) (string, []any) { return "FALSE", nil }

// AllowAll matches every row in the corpus. Only for corpora that carry no
// per-caller restrictions — shared help documentation, say — never as a
// stand-in for "I haven't worked out the scope yet".
func AllowAll(int) (string, []any) { return "TRUE", nil }

// resolve returns f, or DenyAll when f is nil. Every code path that consumes a
// ScopeFilter must go through this: a nil filter is far more likely to be an
// oversight than an intent to expose everything.
func (f ScopeFilter) resolve() ScopeFilter {
	if f == nil {
		return DenyAll
	}
	return f
}

// Clause applies the filter, substituting DenyAll when it is nil.
func (f ScopeFilter) Clause(firstArg int) (string, []any) {
	return f.resolve()(firstArg)
}

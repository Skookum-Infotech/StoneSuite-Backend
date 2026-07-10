package query

// SearchResolver is an optional interface a FieldResolver may implement to power
// a single global-search box. SearchPredicate returns a self-contained SQL
// boolean expression that matches the search term bound at placeholder (e.g.
// "$3"); it supplies its own wildcarding ('%'||$n||'%') and may reference other
// tables via correlated EXISTS. The engine binds the raw term as one parameter
// and ANDs the fragment onto scope+filters, so the OR lives inside the fragment
// and the "filter x scope = AND" invariant is preserved. The fragment is trusted
// per-entity code; it must reference only the given placeholder for the value.
type SearchResolver interface {
	SearchPredicate(placeholder string) string
}

package query

import (
	"strconv"
	"strings"
)

// sortableFields is the v1 allowlist of columns a client may sort by. These are
// stable, non-null columns, which keeps keyset pagination correct (NULLs in a
// sort column would break the (sortValue, id) cursor comparison). Filtering by
// custom fields is fully supported; sorting by them is intentionally deferred.
var sortableFields = map[string]bool{
	"created_at":    true,
	"updated_at":    true,
	"record_number": true,
}

// defaultSort orders newest-first when the client gives no sort directive.
var defaultSort = SortKey{Field: "created_at", Dir: DirDesc}

// Built is the assembled, parameterized SQL for a Request. The store ANDs Where
// and Keyset onto its own scope clause, appends OrderBy and a LIMIT of
// EffLimit+1 (the extra row signals "has more"), and binds Args after its own
// scope parameters.
type Built struct {
	Where    string  // filter predicate, no leading AND; "" when no filters
	Keyset   string  // keyset predicate, no leading AND; "" when no cursor
	OrderBy  string  // e.g. "created_at DESC, id ASC"
	Args     []any   // bound in order, starting at the caller's startIdx
	EffLimit int     // effective page size (store should fetch EffLimit+1)
	Sort     SortKey // the effective sort key (for cursor encoding)
}

// params allocates positional placeholders ($n) starting at a caller offset.
type params struct {
	idx  int
	args []any
}

func (p *params) add(v any) string {
	p.args = append(p.args, v)
	s := "$" + strconv.Itoa(p.idx)
	p.idx++
	return s
}

// buildFilterPreds validates clauses against r's whitelist and returns one
// parameterized predicate per clause (ANDed by the caller). Shared by Build
// (list/search, which also needs sort + keyset) and BuildFilters (count,
// which needs only this).
func buildFilterPreds(clauses []Clause, r FieldResolver, p *params) ([]string, error) {
	var preds []string
	for _, c := range clauses {
		expr, dt, ok := r.Resolve(c.Field)
		if !ok {
			return nil, invalid(c.Field, "unknown field")
		}
		if !opAllowed(dt, c.Op) {
			return nil, invalid(c.Field, "operator "+string(c.Op)+" not allowed for "+string(dt))
		}
		sql, err := clauseSQL(expr, dt, c, p)
		if err != nil {
			return nil, err
		}
		preds = append(preds, sql)
	}
	return preds, nil
}

// BuildFilters validates clauses against r's whitelist and emits a single
// parameterized WHERE predicate (clauses ANDed together, "" if clauses is
// empty) — the filter-only counterpart to Build, for callers that need a
// scope-composed count rather than a sorted, paginated page. startIdx is the
// next free placeholder number, same convention as Build.
func BuildFilters(clauses []Clause, r FieldResolver, startIdx int) (whereSQL string, args []any, err error) {
	p := &params{idx: startIdx}
	preds, err := buildFilterPreds(clauses, r, p)
	if err != nil {
		return "", nil, err
	}
	return strings.Join(preds, " AND "), p.args, nil
}

// Build validates a Request against the resolver's whitelist and emits
// parameterized SQL. startIdx is the next free placeholder number (the store's
// scope clause uses $1..$startIdx-1). It never returns raw client strings as
// SQL; an unknown field, bad operator, or malformed value yields
// *InvalidFilterError (HTTP 400), never a panic or 500.
func Build(req Request, r FieldResolver, startIdx int) (Built, error) {
	p := &params{idx: startIdx}

	// --- filters (ANDed) ---
	preds, err := buildFilterPreds(req.Filters, r, p)
	if err != nil {
		return Built{}, err
	}

	// --- global search (optional) ---
	if req.Search != "" {
		sr, ok := r.(SearchResolver)
		if !ok {
			return Built{}, invalid("search", "search is not supported for this resource")
		}
		// Split on whitespace and require every token to match (AND); the
		// resolver's fragment ORs across its own columns for a single token, so
		// "acme granite" matches a row containing both words in any searched
		// column, in any order. Each token is stripped of LIKE metacharacters
		// (SearchPredicate fragments wildcard the bound value themselves with no
		// ESCAPE clause -- see search.go) and bound as its own parameter, so the
		// "filter x scope = AND" invariant still holds (each token is just one
		// more AND-ed predicate).
		for _, tok := range strings.Fields(req.Search) {
			tok = StripLikeMeta(tok)
			if tok == "" {
				continue
			}
			if frag := sr.SearchPredicate(p.add(tok)); frag != "" {
				preds = append(preds, frag)
			}
		}
	}

	// --- sort (single key + id tiebreaker) ---
	sort, err := effectiveSort(req.Sort, r)
	if err != nil {
		return Built{}, err
	}
	sortExpr, sortDT := sortExprFor(sort.Field, r)
	idExpr, _, ok := r.Resolve("id")
	if !ok {
		return Built{}, invalid("id", "resolver does not expose id")
	}
	dir := "ASC"
	if sort.Dir == DirDesc {
		dir = "DESC"
	}
	orderBy := sortExpr + " " + dir + ", " + idExpr + " ASC"

	// --- keyset (when a cursor is present) ---
	keyset := ""
	if req.Cursor != "" {
		cur, err := decodeCursor(req.Cursor)
		if err != nil {
			return Built{}, err
		}
		if cur.Sort != sort.Field || cur.Dir != sort.Dir {
			return Built{}, invalid("cursor", "cursor does not match the current sort")
		}
		keyset, err = keysetSQL(sortExpr, idExpr, sort.Dir, sortDT, cur, p)
		if err != nil {
			return Built{}, err
		}
	}

	return Built{
		Where:    strings.Join(preds, " AND "),
		Keyset:   keyset,
		OrderBy:  orderBy,
		Args:     p.args,
		EffLimit: effLimit(req.Limit),
		Sort:     sort,
	}, nil
}

// effLimit clamps the page size into [1, MaxLimit], defaulting when unset.
func effLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

// effectiveSort validates the requested sort or falls back to the default.
// v1 honors a single sort key; more than one is rejected explicitly rather
// than silently dropped.
func effectiveSort(keys []SortKey, r FieldResolver) (SortKey, error) {
	if len(keys) == 0 {
		return defaultSort, nil
	}
	if len(keys) > 1 {
		return SortKey{}, invalid("sort", "only one sort key is supported")
	}
	k := keys[0]
	if !isSortable(k.Field, r) {
		return SortKey{}, invalid(k.Field, "field is not sortable")
	}
	if _, _, ok := r.Resolve(k.Field); !ok {
		return SortKey{}, invalid(k.Field, "unknown field")
	}
	if k.Dir != DirAsc && k.Dir != DirDesc {
		k.Dir = DirDesc
	}
	return k, nil
}

// clauseSQL emits one parameterized predicate for a validated clause.
func clauseSQL(expr string, dt DataType, c Clause, p *params) (string, error) {
	switch c.Op {
	case OpIsNull:
		return expr + " IS NULL", nil
	case OpIsEmpty:
		return "(" + expr + " IS NULL OR " + expr + " = '')", nil
	case OpEq:
		v, err := coerceScalar(c.Field, dt, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " = " + p.add(v), nil
	case OpNeq:
		v, err := coerceScalar(c.Field, dt, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " <> " + p.add(v), nil
	case OpGt, OpGte, OpLt, OpLte:
		v, err := coerceScalar(c.Field, dt, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " " + cmpOp(c.Op) + " " + p.add(v), nil
	case OpContains:
		s, err := coerceString(c.Field, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " ILIKE '%' || " + p.add(escapeLike(s)) + " || '%' ESCAPE '\\'", nil
	case OpStartsWith:
		s, err := coerceString(c.Field, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " ILIKE " + p.add(escapeLike(s)) + " || '%' ESCAPE '\\'", nil
	case OpIn:
		list, err := coerceList(c.Field, dt, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " = ANY(" + p.add(list) + ")", nil
	case OpBetween:
		lo, hi, err := coercePair(c.Field, dt, c.Value)
		if err != nil {
			return "", err
		}
		return expr + " BETWEEN " + p.add(lo) + " AND " + p.add(hi), nil
	default:
		return "", invalid(c.Field, "unsupported operator "+string(c.Op))
	}
}

func cmpOp(op Operator) string {
	switch op {
	case OpGt:
		return ">"
	case OpGte:
		return ">="
	case OpLt:
		return "<"
	case OpLte:
		return "<="
	}
	return "="
}

// escapeLike neutralizes LIKE metacharacters so contains/startswith match the
// literal user input (paired with `ESCAPE '\'` in the SQL).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// StripLikeMeta drops LIKE/ILIKE wildcard metacharacters from a free-text search
// token. Unlike escapeLike (which pairs with an `ESCAPE '\'` clause), the
// SearchResolver fragments wildcard the bound value themselves and carry no
// ESCAPE clause, so a stray '%' or '_' typed into the search box would act as a
// wildcard. A search box is a loose contains-match; removing these is simpler and
// less surprising than matching them literally. Exported so hand-rolled searches
// that don't go through Build (e.g. userstore) get the same protection.
func StripLikeMeta(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '%', '_', '\\':
			return -1
		}
		return r
	}, s)
}

// sortExprFor returns the ORDER BY / keyset expression + data type for a sort
// field: from SortResolver when the resolver provides one, else from Resolve
// (the built-in stable columns).
func sortExprFor(field string, r FieldResolver) (string, DataType) {
	if sr, ok := r.(SortResolver); ok {
		if expr, dt, ok := sr.SortExpr(field); ok {
			return expr, dt
		}
	}
	expr, dt, _ := r.Resolve(field)
	return expr, dt
}

// isSortable reports whether a field may be sorted: a built-in stable column or
// one the resolver declares via SortResolver.
func isSortable(field string, r FieldResolver) bool {
	if sortableFields[field] {
		return true
	}
	if sr, ok := r.(SortResolver); ok {
		_, _, ok := sr.SortExpr(field)
		return ok
	}
	return false
}

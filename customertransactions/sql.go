package customertransactions

import (
	"strconv"
	"strings"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/dateonly"
)

// Page-size bounds, mirroring mytransactions and auditstore.
const (
	MaxLimit     = 100
	DefaultLimit = 25
)

// MaxPage bounds how deep a caller can page, keeping OFFSET sane.
const MaxPage = 10_000

// argCustomer is the bind position fixed for every statement: the customer's
// internal id. Every other value is appended lazily after it.
const argCustomer = "$1"

// granted is a Source the caller may read, with whether that read grant is
// narrowed to records the caller owns.
type granted struct {
	Source
	own bool
}

// binder accumulates positional arguments after the customer id. Today and the
// employee id bind lazily and once: Postgres rejects a bind parameter the
// statement never references, so an unused value must never be sent.
type binder struct {
	args     []any
	todayVal time.Time
	empVal   int
	todayArg string
	empArg   string
}

// newBinder seeds the customer id as $1.
func newBinder(customerID, employee int, today time.Time) *binder {
	return &binder{args: []any{customerID}, todayVal: today, empVal: employee}
}

// add binds v and returns its placeholder.
func (b *binder) add(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// today binds the tenant-local calendar day once and returns its placeholder.
func (b *binder) today() string {
	if b.todayArg == "" {
		b.todayArg = b.add(dateonly.New(b.todayVal)) + "::date"
	}
	return b.todayArg
}

// emp binds the caller's employee id once and returns its placeholder.
func (b *binder) emp() string {
	if b.empArg == "" {
		b.empArg = b.add(b.empVal)
	}
	return b.empArg
}

// branchSQL is one document type's SELECT in the shared union shape. Every
// identifier and expression comes from the static registry; the customer, the
// employee and today are bound through b.
func (g granted) branchSQL(b *binder) string {
	opt := func(expr, cast string) string {
		if expr == "" {
			return "NULL::" + cast
		}
		return "(" + expr + ")::" + cast
	}
	overdue := "FALSE"
	if g.Overdue {
		overdue = "COALESCE((" + g.OpenWhere + ") AND " + g.DueDate + " < " + b.today() + ", FALSE)"
	}
	where := []string{
		"t." + g.DeletedAt + " IS NULL",
		"t." + g.CustomerCol + " = " + argCustomer,
	}
	if g.own {
		where = append(where, "t."+g.Owner+" = "+b.emp())
	}
	return "SELECT '" + g.Key + "'::text AS type," +
		" " + g.ID + "::text AS id," +
		" COALESCE(" + g.Number + ", '')::text AS number," +
		" COALESCE(" + statusAlias + ".record_status_name, '')::text AS status_name," +
		" COALESCE(" + statusCodeExpr + ", '')::text AS status_code," +
		" (" + g.Date + ")::date AS doc_date," +
		" " + opt(g.DueDate, "date") + " AS due_date," +
		" " + opt(g.Amount, "float8") + " AS amount," +
		" " + opt(g.Balance, "float8") + " AS balance," +
		" COALESCE((" + g.OpenWhere + "), FALSE) AS is_open," +
		" " + overdue + " AS is_overdue," +
		" t." + g.CreatedAt + "::timestamp AS created_at," +
		" t." + g.UpdatedAt + "::timestamp AS updated_at" +
		" FROM " + g.Table + " t" +
		" LEFT JOIN lkp_record_status " + statusAlias + " ON " + statusAlias + ".record_status_id = t." + g.StatusCol +
		" WHERE " + strings.Join(where, " AND ")
}

// unionSQL joins every granted type's branch into one set.
func unionSQL(sources []granted, b *binder) string {
	branches := make([]string, len(sources))
	for i, g := range sources {
		branches[i] = g.branchSQL(b)
	}
	return strings.Join(branches, " UNION ALL ")
}

// pageFilter is the validated, narrowing part of a page request. The type
// filter is applied by narrowing the sources, not in SQL.
type pageFilter struct {
	status   string         // status code; "" for any
	from     *dateonly.Date // doc_date lower bound, inclusive
	to       *dateonly.Date // doc_date upper bound, inclusive
	search   string         // number contains-match, already stripped of LIKE metacharacters
	openOnly bool
	limit    int // page size
	offset   int // rows to skip: (page-1) * limit
}

// whereSQL renders the filter predicates over the union ("" when the request
// does not narrow it). Every value is bound, never interpolated; predicates are
// ANDed.
func whereSQL(f pageFilter, b *binder) string {
	var conds []string
	if f.status != "" {
		conds = append(conds, "status_code = "+b.add(f.status))
	}
	if f.from != nil {
		conds = append(conds, "doc_date >= "+b.add(*f.from)+"::date")
	}
	if f.to != nil {
		conds = append(conds, "doc_date <= "+b.add(*f.to)+"::date")
	}
	if f.search != "" {
		conds = append(conds, "number ILIKE "+b.add("%"+f.search+"%"))
	}
	if f.openOnly {
		conds = append(conds, "is_open")
	}
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// buildPageSQL assembles one newest-first page over sources and its arguments
// (customer id first). Each row also carries the filtered total (COUNT(*) OVER
// ()) so one scan yields both the page and the page count. (doc_date DESC,
// created_at DESC, type, id) is a total order, so pages never overlap or skip
// while the data holds still. All dynamic values are bound.
func buildPageSQL(sources []granted, f pageFilter, customerID, employee int, today time.Time) (string, []any) {
	b := newBinder(customerID, employee, today)
	union := unionSQL(sources, b)
	where := whereSQL(f, b)
	limit := b.add(f.limit)
	offset := b.add(f.offset)

	sql := "SELECT type, id, number, status_name, status_code, doc_date, due_date, amount, balance," +
		" is_open, is_overdue, created_at, updated_at, COUNT(*) OVER () AS total" +
		" FROM (" + union + ") x" + where +
		" ORDER BY doc_date DESC, created_at DESC, type ASC, id ASC LIMIT " + limit + " OFFSET " + offset
	return sql, b.args
}

// buildCountSQL assembles the filtered total on its own, for a page past the
// end of the list (which returns no rows, hence no windowed total).
func buildCountSQL(sources []granted, f pageFilter, customerID, employee int, today time.Time) (string, []any) {
	b := newBinder(customerID, employee, today)
	union := unionSQL(sources, b)
	where := whereSQL(f, b)
	return "SELECT COUNT(*) FROM (" + union + ") x" + where, b.args
}

// grantFor resolves the caller's read decision on a source into a granted
// source; ok is false when the caller may not read it.
func grantFor(s Source, grants []authz.Grant) (granted, bool) {
	d := authz.DecideAny(grants, []authz.Resource{s.Resource}, authz.ActionRead)
	if !d.Allowed {
		return granted{}, false
	}
	// Fail closed: only an explicit "all" lifts the owner narrowing.
	return granted{Source: s, own: d.Scope != authz.ScopeAll}, true
}

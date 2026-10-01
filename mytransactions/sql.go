package mytransactions

import (
	"strconv"
	"strings"
	"time"

	"stonesuite-backend/authz"
)

// Page-size bounds, mirroring the record query engine and auditstore.
const (
	MaxLimit     = 100
	DefaultLimit = 25
)

// MaxPage bounds how deep a caller can page, keeping OFFSET sane.
const MaxPage = 10_000

// timestampLayout is how the "since" bound travels: the tables store
// `timestamp` (no zone), so it is bound as text and cast ::timestamp, never as
// a zone-carrying time.Time.
const timestampLayout = "2006-01-02 15:04:05.999999"

// Bind positions fixed for every statement: the caller's employee id and
// tenant user id. Dynamic values are appended after them.
const (
	argEmployee = "$1"
	argUser     = "$2"
)

// auditUpdateActions are the audit_logs actions that count as "the caller
// updated this record" for modules with no updated_by column.
const auditUpdateActions = "'update','transition','approve','reject'"

// granted is a Source the caller may read, with whether that read grant is
// narrowed to records the caller owns.
type granted struct {
	Source
	own bool
}

// ownerPredicate narrows an own-scoped read to records the caller owns, exactly
// as the module's own list does. A module with no owner column cannot narrow,
// so its read grant sees every row (see Source.Owner).
func (g granted) ownerPredicate() string {
	if !g.own || g.Owner == "" {
		return ""
	}
	return "t." + g.Owner + " = " + argEmployee
}

// createdByMe is true when the caller created the record.
func (g granted) createdByMe() string {
	return "t." + g.CreatedBy + " = " + argEmployee
}

// updatedByMe is true when the caller last updated the record (or, for CRM,
// has updated it at any point — the table keeps no updated_by).
func (g granted) updatedByMe() string {
	switch {
	case g.AuditUpdated:
		return "EXISTS (SELECT 1 FROM audit_logs al" +
			" WHERE al.resource_id = " + g.ID + "::text" +
			" AND al.actor_user_id = " + argUser + "::uuid" +
			" AND al.action IN (" + auditUpdateActions + "))"
	case g.UpdatedBy != "":
		return "t." + g.UpdatedBy + " = " + argEmployee
	default:
		return "FALSE"
	}
}

// textExpr renders an optional text expression as a non-null text column.
func textExpr(expr string) string {
	if expr == "" {
		return "''::text"
	}
	return "COALESCE(" + expr + ", '')::text"
}

// branchSQL is one module's SELECT in the shared union shape. Every
// identifier and expression comes from the static registry.
func (g granted) branchSQL() string {
	amount := "NULL::float8"
	if g.Amount != "" {
		amount = "(" + g.Amount + ")::float8"
	}
	where := []string{
		"t." + g.DeletedAt + " IS NULL",
		"(" + g.createdByMe() + " OR " + g.updatedByMe() + ")",
	}
	if g.Where != "" {
		where = append(where, g.Where)
	}
	if p := g.ownerPredicate(); p != "" {
		where = append(where, p)
	}
	from := g.Table + " t"
	if g.Joins != "" {
		from += " " + g.Joins
	}
	return "SELECT '" + g.Key + "'::text AS type," +
		" " + g.ID + "::text AS id," +
		" " + textExpr(g.Number) + " AS number," +
		" " + textExpr(g.Name) + " AS name," +
		" " + textExpr(g.Account) + " AS account," +
		" " + textExpr(g.StatusName) + " AS status_name," +
		" " + textExpr(g.StatusCode) + " AS status_code," +
		" " + amount + " AS amount," +
		" COALESCE(" + g.createdByMe() + ", FALSE) AS created_by_me," +
		" t." + g.UpdatedAt + "::timestamp AS updated_at," +
		" t." + g.CreatedAt + "::timestamp AS created_at" +
		" FROM " + from +
		" WHERE " + strings.Join(where, " AND ")
}

// unionSQL joins every granted module's branch into the shared "mine" set.
func unionSQL(sources []granted) string {
	branches := make([]string, len(sources))
	for i, g := range sources {
		branches[i] = g.branchSQL()
	}
	return strings.Join(branches, " UNION ALL ")
}

// pageFilter is the validated, narrowing part of a page request.
type pageFilter struct {
	role   string // RoleAll | RoleCreated | RoleUpdated
	search string // already stripped of LIKE metacharacters; "" for none
	limit  int    // page size
	offset int    // rows to skip: (page-1) * limit
}

// binder accumulates positional arguments after the fixed ones.
type binder struct{ args []any }

// newBinder seeds the fixed arguments: the employee id as $1 and, only when
// some source reads the audit trail, the tenant user id as $2. Postgres rejects
// a bind parameter the statement never references, so $2 is sent only when used.
func newBinder(sources []granted, employeeID int, userID *string) *binder {
	b := &binder{args: []any{employeeID}}
	for _, g := range sources {
		if g.AuditUpdated {
			b.args = append(b.args, userID)
			break
		}
	}
	return b
}

func (b *binder) add(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// whereSQL renders the role and search predicates over the union ("" when the
// request does not narrow it). The search term is bound, never interpolated.
func whereSQL(f pageFilter, b *binder) string {
	var conds []string
	switch f.role {
	case RoleCreated:
		conds = append(conds, "created_by_me")
	case RoleUpdated:
		conds = append(conds, "NOT created_by_me")
	}
	if f.search != "" {
		p := b.add("%" + f.search + "%")
		conds = append(conds, "(number ILIKE "+p+" OR name ILIKE "+p+" OR account ILIKE "+p+")")
	}
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// buildPageSQL assembles one newest-first page over sources and its arguments
// (employee id, then user id when needed, first). Each row also carries the
// filtered total (COUNT(*) OVER ()) so one scan yields both the page and the
// page count. (updated_at DESC, type, id) is a total order, so pages never
// overlap or skip while the data holds still. All dynamic values are bound.
func buildPageSQL(sources []granted, f pageFilter, employeeID int, userID *string) (string, []any) {
	b := newBinder(sources, employeeID, userID)
	where := whereSQL(f, b)
	limit := b.add(f.limit)
	offset := b.add(f.offset)

	sql := "SELECT type, id, number, name, account, status_name, status_code, amount, created_by_me," +
		" updated_at, created_at, COUNT(*) OVER () AS total" +
		" FROM (" + unionSQL(sources) + ") mine" + where +
		" ORDER BY updated_at DESC, type ASC, id ASC LIMIT " + limit + " OFFSET " + offset
	return sql, b.args
}

// buildCountSQL assembles the filtered total on its own, for a page past the
// end of the list (which returns no rows, hence no windowed total).
func buildCountSQL(sources []granted, f pageFilter, employeeID int, userID *string) (string, []any) {
	b := newBinder(sources, employeeID, userID)
	where := whereSQL(f, b)
	return "SELECT COUNT(*) FROM (" + unionSQL(sources) + ") mine" + where, b.args
}

// buildSummarySQL assembles the stat-card counts over every readable module:
// total, created by the caller, updated-but-not-created, and touched since
// the given instant.
func buildSummarySQL(sources []granted, since time.Time, employeeID int, userID *string) (string, []any) {
	b := newBinder(sources, employeeID, userID)
	p := b.add(since.UTC().Format(timestampLayout))
	sql := "SELECT COUNT(*), COUNT(*) FILTER (WHERE created_by_me)," +
		" COUNT(*) FILTER (WHERE NOT created_by_me)," +
		" COUNT(*) FILTER (WHERE updated_at >= " + p + "::timestamp)" +
		" FROM (" + unionSQL(sources) + ") mine"
	return sql, b.args
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

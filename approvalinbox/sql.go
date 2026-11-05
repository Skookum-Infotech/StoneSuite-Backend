package approvalinbox

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"stonesuite-backend/approvalchain"
)

// userStatusActive is users.status for a live user (same value LiveApproverJoin hardcodes).
const userStatusActive = "active"

// Scope selects whose queue is listed.
type Scope string

const (
	// ScopeMine lists records the caller can still sign: an active approver for the
	// record's current status who has not yet signed it.
	ScopeMine Scope = "mine"
	// ScopeAll lists every pending record at a gate. Super admins only.
	ScopeAll Scope = "all"
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool { return s == ScopeMine || s == ScopeAll }

// Source is one module that passed the caller's RBAC read check.
type Source struct {
	Key string
	// OwnOnly restricts rows to OwnerColumn = caller employee (RBAC scope "own").
	OwnOnly bool
	// CanTransition is the caller's authz transition check for the module's resource.
	CanTransition bool
}

var errNoSources = errors.New("approvalinbox: no permitted modules")

// builder accumulates positional parameters. $1..$3 are shared by every branch.
type builder struct {
	args []any
}

const (
	pEmployee = "$1"
	pPending  = "$2"
	pActive   = "$3"
)

// newBuilder starts a parameter list with the caller's employee id ($1) and the pending
// approval status ($2). withActive also binds the live-user status as $3 (the page's
// required-approvals expression uses it). A parameter the SQL never references makes
// Postgres reject the statement, so each statement binds only what it uses.
func newBuilder(employeeID int, withActive bool) *builder {
	args := []any{employeeID, approvalchain.StatusPending}
	if withActive {
		args = append(args, userStatusActive)
	}
	return &builder{args: args}
}

func (b *builder) param(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// module bundles the registry config and inbox metadata of one source.
type module struct {
	src  Source
	cfg  approvalchain.ModuleConfig
	meta Meta
}

func resolveModule(src Source) (module, error) {
	cfg, ok := approvalchain.ForWorkflowKey(src.Key)
	if !ok {
		return module{}, fmt.Errorf("approvalinbox: %q not in approvalchain registry", src.Key)
	}
	meta, ok := metas[src.Key]
	if !ok {
		return module{}, fmt.Errorf("approvalinbox: %q has no inbox metadata", src.Key)
	}
	if msg, ok := identsOK(src.Key, cfg, meta); !ok {
		return module{}, errors.New("approvalinbox: " + msg)
	}
	if src.OwnOnly && cfg.Record.OwnerColumn == "" {
		return module{}, fmt.Errorf("approvalinbox: %q has no owner column for own scope", src.Key)
	}
	return module{src: src, cfg: cfg, meta: meta}, nil
}

func (m module) replacer(extra ...string) *strings.Replacer {
	r := m.cfg.Record
	pairs := []string{
		"{t}", r.Table, "{h}", r.HistoryTable, "{id}", r.IDColumn, "{uuid}", r.UUIDColumn,
		"{st}", r.StatusColumn, "{ap}", r.ApprovalStatusColumn, "{del}", r.DeletedAtColumn,
		"{cr}", r.CreatedAtColumn, "{own}", r.OwnerColumn, "{apr}", m.cfg.ApproverTable,
		"{apl}", m.cfg.ApprovalTable, "{live}", approvalchain.LiveApproverJoin,
	}
	return strings.NewReplacer(append(pairs, extra...)...)
}

// enteredAtExpr is the instant the record entered its current status: the latest history
// row into it with from <> to (self-transitions such as partial sign-offs don't restart
// the clock -- same expression as automation/approval_source.go), else created_at.
const enteredAtExpr = `COALESCE((SELECT MAX(h.at) FROM {h} h WHERE h.{id} = d.{id} AND h.to_status_id = d.{st}
    AND h.from_status_id IS DISTINCT FROM h.to_status_id), d.{cr})`

// isApproverExpr: the caller is an active, live approver for the record's current status.
const isApproverExpr = `EXISTS (SELECT 1 FROM {apr} ea {live}
    WHERE ea.record_type_id = rt.record_type_id AND ea.record_status_id = rs.record_status_id
      AND ea.approver_employee_id = ` + pEmployee + ` AND ea.is_active)`

// signedByCallerExpr: the caller already has a sign-off row for the record's current status.
const signedByCallerExpr = `EXISTS (SELECT 1 FROM {apl} s WHERE s.{id} = d.{id} AND s.record_status_id = d.{st}
    AND s.approver_employee_id = ` + pEmployee + `)`

// requiredExpr mirrors approvalchain.RequiredApprovals over the LEFT JOIN form of
// approverCounts: live approvers, or 1 for an orphaned gate, or 0 when ungated.
const requiredExpr = `(SELECT (CASE WHEN COUNT(u.id) > 0 THEN COUNT(u.id) WHEN COUNT(*) > 0 THEN 1 ELSE 0 END)::int
    FROM {apr} ea
    LEFT JOIN employee e ON e.employee_id = ea.approver_employee_id AND e.employee_deleted_at IS NULL
    LEFT JOIN users u ON u.id = e.employee_user_id AND u.status = ` + pActive + `
    WHERE ea.record_type_id = rt.record_type_id AND ea.record_status_id = rs.record_status_id AND ea.is_active)`

// approvedExpr mirrors approvalchain.signOffCount: sign-offs by approvers who are still
// active configured approvers and live users.
const approvedExpr = `(SELECT COUNT(*)::int FROM {apl} s WHERE s.{id} = d.{id} AND s.record_status_id = d.{st}
    AND EXISTS (SELECT 1 FROM {apr} ea {live}
      WHERE ea.record_type_id = rt.record_type_id AND ea.record_status_id = s.record_status_id
        AND ea.approver_employee_id = s.approver_employee_id AND ea.is_active))`

// fromWhere is the one place the inbox predicate lives; the page and the count
// statements both use it, so the KPI tile and the inbox total cannot drift.
func (m module) fromWhere(b *builder, scope Scope, withOwnerJoin bool) string {
	code := b.param(m.cfg.RecordTypeCode)
	gates := make([]string, 0, len(m.cfg.Gates))
	for _, g := range m.cfg.Gates {
		gates = append(gates, g.StatusCode)
	}
	gateParam := b.param(gates)

	var sb strings.Builder
	sb.WriteString(`FROM {t} d
JOIN lkp_record_status rs ON rs.record_status_id = d.{st}
JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type AND rt.record_type_code = ` + code + `
`)
	if withOwnerJoin && m.cfg.Record.OwnerColumn != "" {
		sb.WriteString(`LEFT JOIN employee oe ON oe.employee_id = d.{own}
LEFT JOIN users ou ON ou.id = oe.employee_user_id
`)
	}
	sb.WriteString(`WHERE d.{del} IS NULL AND d.{ap} = ` + pPending + ` AND rs.record_status_code = ANY(` + gateParam + `::text[])`)
	if m.src.OwnOnly {
		sb.WriteString(` AND d.{own} = ` + pEmployee)
	}
	if scope == ScopeMine {
		sb.WriteString(` AND ` + isApproverExpr + ` AND NOT ` + signedByCallerExpr)
	} else if !m.src.OwnOnly {
		// $1 is always bound; Postgres rejects a statement with an unreferenced parameter
		// (SQLSTATE 42P18), so scope=all references it with a typed always-true test.
		sb.WriteString(` AND ` + pEmployee + `::int IS NOT NULL`)
	}
	return m.replacer().Replace(sb.String())
}

func (m module) pageBranch(b *builder, scope Scope) string {
	key := b.param(m.src.Key)
	r := m.cfg.Record
	number, owner, party, amount := `''::text`, `''::text`, `''::text`, `''::text`
	if r.NumberColumn != "" {
		number = `COALESCE(d.` + r.NumberColumn + `::text, '')`
	}
	if r.OwnerColumn != "" {
		owner = `COALESCE(NULLIF(ou.full_name, ''), ou.email, '')`
	}
	if m.meta.PartyNameColumn != "" {
		party = `COALESCE(d.` + m.meta.PartyNameColumn + `::text, '')`
	}
	if m.meta.AmountColumn != "" {
		amount = `COALESCE(d.` + m.meta.AmountColumn + `::text, '')`
	}
	sel := `SELECT ` + key + `::text AS workflow_key, d.{uuid}::text AS uuid, ` + enteredAtExpr + ` AS entered_at,
  ` + number + ` AS number, rs.record_status_code AS status_code, rs.record_status_name AS status_name,
  ` + owner + ` AS owner_name, ` + party + ` AS party_name, ` + amount + ` AS amount,
  ` + requiredExpr + ` AS required_approvals, ` + approvedExpr + ` AS approved_count,
  ` + isApproverExpr + ` AS is_approver
`
	return m.replacer().Replace(sel) + m.fromWhere(b, scope, true)
}

func (m module) countBranch(b *builder, scope Scope) string {
	key := b.param(m.src.Key)
	sel := `SELECT ` + key + `::text AS workflow_key, ` + enteredAtExpr + ` AS entered_at
`
	return m.replacer().Replace(sel) + m.fromWhere(b, scope, false)
}

func resolveModules(sources []Source) ([]module, error) {
	if len(sources) == 0 {
		return nil, errNoSources
	}
	mods := make([]module, 0, len(sources))
	for _, s := range sources {
		m, err := resolveModule(s)
		if err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// pageQuery is one page request over the permitted (and optionally module-filtered) sources.
type pageQuery struct {
	Sources    []Source
	Scope      Scope
	EmployeeID int
	Cursor     *Cursor
	Limit      int // rows to fetch (page size + 1)
}

// buildPage returns the single UNION ALL statement for one page, in keyset order.
func buildPage(q pageQuery) (string, []any, error) {
	mods, err := resolveModules(q.Sources)
	if err != nil {
		return "", nil, err
	}
	b := newBuilder(q.EmployeeID, true)
	branches := make([]string, 0, len(mods))
	for _, m := range mods {
		branches = append(branches, m.pageBranch(b, q.Scope))
	}
	var sb strings.Builder
	sb.WriteString("WITH inbox AS (\n" + strings.Join(branches, "\nUNION ALL\n") + ")\n")
	sb.WriteString(`SELECT workflow_key, uuid, entered_at, number, status_code, status_name, owner_name,
  party_name, amount, required_approvals, approved_count, is_approver
FROM inbox
`)
	if q.Cursor != nil {
		ts, key, id := b.param(q.Cursor.EnteredAt.UTC()), b.param(q.Cursor.WorkflowKey), b.param(q.Cursor.UUID)
		sb.WriteString(`WHERE (entered_at, workflow_key, uuid) > (` + ts + `::timestamp, ` + key + `::text, ` + id + `::text)
`)
	}
	sb.WriteString(`ORDER BY entered_at ASC, workflow_key ASC, uuid ASC
LIMIT ` + b.param(q.Limit))
	return sb.String(), b.args, nil
}

// buildCount returns the statement counting the same rows per module (no cursor, no module filter).
func buildCount(sources []Source, scope Scope, employeeID int) (string, []any, error) {
	mods, err := resolveModules(sources)
	if err != nil {
		return "", nil, err
	}
	b := newBuilder(employeeID, false)
	branches := make([]string, 0, len(mods))
	for _, m := range mods {
		branches = append(branches, m.countBranch(b, scope))
	}
	sql := "SELECT workflow_key, COUNT(*)::int, MIN(entered_at) FROM (\n" +
		strings.Join(branches, "\nUNION ALL\n") + ") c GROUP BY workflow_key"
	return sql, b.args, nil
}

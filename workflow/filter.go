package workflow

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/query"
)

// recordResolver implements query.FieldResolver for the v1 JSONB record store.
// It is the security whitelist: only keys it resolves can appear in a filter,
// and every SQL expression it returns is a fixed, injection-safe fragment
// (custom/core field keys are regex-validated identifiers, never interpolated
// client text). Values are bound as parameters by the query builder.
//
// Logical key namespaces:
//   - bare system keys: id, created_at, updated_at, record_number, status,
//     owner_user_id, team_id  -> real columns
//   - "cf:<key>"  -> custom_fields JSONB, typed per the field definition
//   - "core:<key>" -> core_fields JSONB, treated as text
type recordResolver struct {
	defs map[string]FieldDefinition // custom field key -> definition
}

// newRecordResolver builds a resolver from a workflow's custom field defs.
func newRecordResolver(defs []FieldDefinition) recordResolver {
	m := make(map[string]FieldDefinition, len(defs))
	for _, d := range defs {
		m[d.Key] = d
	}
	return recordResolver{defs: m}
}

// systemFields maps bare keys to (column expression, type).
var systemFields = map[string]struct {
	expr string
	dt   query.DataType
}{
	"id":            {"id::text", query.TypeString},
	"created_at":    {"created_at", query.TypeDate},
	"updated_at":    {"updated_at", query.TypeDate},
	"record_number": {"COALESCE(record_number, '')", query.TypeString},
	"status":        {"current_state_id::text", query.TypeString},
	"owner_user_id": {"owner_user_id::text", query.TypeString},
	"team_id":       {"team_id::text", query.TypeString},
}

// Resolve maps a logical field key to a SQL expression + data type.
func (r recordResolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok {
		d, ok := r.defs[k]
		if !ok {
			return "", "", false
		}
		return jsonbExpr("custom_fields", d.Key, d.DataType), query.DataType(d.DataType), true
	}
	if k, ok := strings.CutPrefix(key, "core:"); ok {
		// Core fields have no schema; accept only safe identifier keys so the
		// key is never an injection vector, and treat the value as text.
		if !validFieldKey.MatchString(k) {
			return "", "", false
		}
		return jsonbExpr("core_fields", k, TypeString), query.TypeString, true
	}
	return "", "", false
}

// jsonbExpr builds a typed JSONB text-extraction expression. The key is a
// validated identifier (custom keys pass validFieldKey at creation; core keys
// are checked in Resolve), so interpolation here is injection-safe.
func jsonbExpr(column, key string, dt DataType) string {
	base := fmt.Sprintf("%s->>'%s'", column, key)
	switch dt {
	case TypeNumber:
		return "(" + base + ")::numeric"
	case TypeDate:
		return "(" + base + ")::timestamptz"
	case TypeBool:
		return "(" + base + ")::boolean"
	default: // string, email, enum
		return base
	}
}

// Page is one keyset-paginated slice of records plus its continuation cursor.
type Page struct {
	Records    []Record `json:"records"`
	NextCursor string   `json:"nextCursor,omitempty"`
	HasMore    bool     `json:"hasMore"`
}

// ListRecordsFiltered returns a scope-filtered, optionally-filtered/sorted,
// keyset-paginated page of records. The RBAC scope clause is ANDed onto the
// caller-supplied filter, so a filter can only narrow the result set — never
// reach records outside the caller's scope. defs are the workflow's custom
// field definitions (drive the filter whitelist + casts).
func ListRecordsFiltered(ctx context.Context, q Querier, workflowID, scope, callerUserID string, defs []FieldDefinition, req query.Request) (Page, error) {
	sql, args, built, err := buildRecordQuery(workflowID, scope, callerUserID, defs, req)
	if err != nil {
		return Page{}, err // *query.InvalidFilterError -> 400 at the controller
	}

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list records filtered: %w", err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("list records filtered: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit { // the n+1 row signals "more"
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortFieldValue(last, built.Sort.Field))
	}
	return page, nil
}

// buildRecordQuery assembles the scope-composed, parameterized SQL for a
// filtered record list. It is pure (no DB) so the scope-AND-never-widen
// invariant and parameter ordering are unit-testable. $1 = workflow_id, then
// scope params, then the filter builder's params.
func buildRecordQuery(workflowID, scope, callerUserID string, defs []FieldDefinition, req query.Request) (string, []any, query.Built, error) {
	where := []string{"workflow_id = $1"}
	args := []any{workflowID}
	nextIdx := 2
	switch scope {
	case "all":
		// no narrowing
	default: // own
		where = append(where, "owner_user_id = $2")
		args = append(args, nullIfEmpty(callerUserID))
		nextIdx = 3
	}

	built, err := query.Build(req, newRecordResolver(defs), nextIdx)
	if err != nil {
		return "", nil, query.Built{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	sql := fmt.Sprintf("SELECT %s FROM workflow_records WHERE %s ORDER BY %s LIMIT %d",
		recordColumns, strings.Join(where, " AND "), built.OrderBy, built.EffLimit+1)
	return sql, args, built, nil
}

// sortFieldValue extracts the value of the effective sort field from a record,
// for minting the next cursor. Only the sortable system fields are handled.
func sortFieldValue(rec Record, field string) any {
	switch field {
	case "updated_at":
		return rec.UpdatedAt
	case "record_number":
		return rec.RecordNumber
	default: // created_at (the default sort)
		return rec.CreatedAt
	}
}

// ensure recordResolver satisfies the interface at compile time.
var _ query.FieldResolver = recordResolver{}

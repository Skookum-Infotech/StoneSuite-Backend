package crmstore

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// relationalResolver implements query.FieldResolver for the DesignV2 customer
// table. It is the filter whitelist: system fields and the registry-mapped core
// columns map to fixed, injection-safe column expressions; custom fields read
// the customer_custom_fields JSONB with a regex-validated key. Every value is
// bound as a parameter by the query builder.
type relationalResolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// relationalSystemFields maps bare logical keys to customer columns.
var relationalSystemFields = map[string]resolved{
	"id":            {"c.customer_uuid::text", query.TypeString},
	"created_at":    {"c.customer_created_at", query.TypeDate},
	"updated_at":    {"c.customer_updated_at", query.TypeDate},
	"status":        {"c.customer_crm_status::text", query.TypeString},
	"record_number": {"COALESCE(c.customer_doc_num,'')", query.TypeString},
}

// relationalCoreFields is derived once from the customer column registry so the
// filterable core surface stays in lock-step with reads/writes.
var relationalCoreFields = buildRelationalCoreFields()

func buildRelationalCoreFields() map[string]resolved {
	m := make(map[string]resolved, len(customerFields))
	for _, f := range customerFields {
		var r resolved
		switch f.kind {
		case kStr:
			r = resolved{"c." + f.col, query.TypeString}
		case kBool:
			r = resolved{"c." + f.col, query.TypeBool}
		case kDec:
			r = resolved{"c." + f.col, query.TypeNumber}
		case kDate:
			r = resolved{"c." + f.col, query.TypeDate}
		default: // kFK, kInt — ids stored as integers, filtered as text
			r = resolved{"c." + f.col + "::text", query.TypeString}
		}
		m[f.core] = r
	}
	return m
}

// validCustomKey mirrors workflow.validFieldKey so JSONB custom keys are safe to
// interpolate (the regex bounds them to identifier characters).
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Resolve maps a logical field key to a SQL expression + data type.
func (relationalResolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := relationalSystemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "core:"); ok {
		if c, ok := relationalCoreFields[k]; ok {
			return c.expr, c.dt, true
		}
		return "", "", false
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok {
		if !validCustomKey.MatchString(k) {
			return "", "", false
		}
		return "c.customer_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = relationalResolver{}

// SearchRecords implements scope-safe filtering + keyset pagination for DesignV2.
// The base predicate pins the record type and excludes soft-deleted rows; the
// RBAC scope (own/team -> caller's owned rows) is ANDed before the client
// filter. NOTE: the v2 customer table has no team column, so "team" scope
// behaves like "own" (matching the existing ListRecords) until team support is
// added to the relational design.
func (s *relationalStore) SearchRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string, req query.Request) (workflow.Page, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return workflow.Page{}, ClientError{Msg: "Unknown CRM workflow: " + key}
	}

	where := []string{"rt.record_type_code = $1", "c.customer_deleted_at IS NULL"}
	args := []any{code}
	nextIdx := 2
	if scope == "own" || scope == "team" {
		empID, found := s.employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return workflow.Page{}, nil // no employee row => no records in scope
		}
		where = append(where, "c.customer_crm_owner_user_id = $2")
		args = append(args, empID)
		nextIdx = 3
	}

	built, err := query.Build(req, relationalResolver{}, nextIdx)
	if err != nil {
		return workflow.Page{}, err // *query.InvalidFilterError -> 400
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	q := recordSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return workflow.Page{}, fmt.Errorf("search customer records: %w", err)
	}
	defer rows.Close()
	out := []workflow.Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return workflow.Page{}, err
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return workflow.Page{}, fmt.Errorf("search customer records: %w", err)
	}

	page := workflow.Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, relationalSortValue(last, built.Sort.Field))
	}
	return page, nil
}

// relationalSortValue reads the effective sort field's value from a record to
// mint the next cursor. Mirrors the sortable allowlist in the query builder.
func relationalSortValue(rec workflow.Record, field string) any {
	switch field {
	case "updated_at":
		return rec.UpdatedAt
	case "record_number":
		return rec.RecordNumber
	default: // created_at (default)
		return rec.CreatedAt
	}
}

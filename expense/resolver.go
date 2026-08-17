package expense

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors requisition.validCustomKey / estimate.validCustomKey
// so JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// expense claims. Table alias `exp` matches expSelect (store_get.go).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist.
var systemFields = map[string]resolved{
	"id":              {"exp.expense_uuid::text", query.TypeString},
	"document_number": {"COALESCE(exp.expense_number,'')", query.TypeString},
	"record_number":   {"COALESCE(exp.expense_number,'')", query.TypeString},
	"status":          {"exp.expense_status::text", query.TypeString},
	"claimant_id":     {"exp.expense_claimant_id::text", query.TypeString},
	"department":      {"exp.expense_department", query.TypeString},
	"total":           {"exp.expense_total", query.TypeNumber},
	"created_by":      {"exp.expense_created_by::text", query.TypeString},
	"updated_by":      {"exp.expense_updated_by::text", query.TypeString},
	"created_at":      {"exp.expense_created_at", query.TypeDate},
	"updated_at":      {"exp.expense_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "exp.expense_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number.
var sortableFields = map[string]resolved{
	"document_number": {"exp.expense_number", query.TypeString},
	"record_number":   {"exp.expense_number", query.TypeString},
	"total":           {"exp.expense_total", query.TypeNumber},
	"status":          {"exp.expense_status", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: document number,
// department, memo, and line description (child, correlated EXISTS).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"exp.expense_number ILIKE '%'||" + ph + "||'%'" +
		" OR exp.expense_department ILIKE '%'||" + ph + "||'%'" +
		" OR exp.expense_memo ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM expense_item ei WHERE ei.expense_id = exp.expense_id" +
		"   AND ei.description ILIKE '%'||" + ph + "||'%')" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

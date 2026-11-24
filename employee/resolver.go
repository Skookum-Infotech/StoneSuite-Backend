package employee

import (
	"stonesuite-backend/query"
)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for employees. Table alias "e" = employee, "rs" = the
// status lookup (see empSelect).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// statusCodeExpr is the employee's status code, falling back to employee_is_active
// for a row that predates the module and has no status yet.
const statusCodeExpr = "COALESCE(rs.record_status_code, CASE WHEN e.employee_is_active THEN 'ACTV' ELSE 'INAC' END)"

// nameExpr is the person's name as the list shows it.
const nameExpr = "LOWER(TRIM(e.employee_first_name || ' ' || e.employee_last_name))"

// systemFields is the filter whitelist. "status_code" is what a list's status
// chips filter on. Every sortFields key must also be listed here: the query
// builder rejects a sort key that Resolve does not know.
var systemFields = map[string]resolved{
	"id":              {"e.employee_uuid::text", query.TypeString},
	"document_number": {"COALESCE(e.employee_number,'')", query.TypeString},
	"record_number":   {"COALESCE(e.employee_number,'')", query.TypeString},
	"status":          {"COALESCE(e.employee_status, 0)::text", query.TypeString},
	"status_code":     {statusCodeExpr, query.TypeString},
	"first_name":      {"e.employee_first_name", query.TypeString},
	"last_name":       {"e.employee_last_name", query.TypeString},
	"name":            {nameExpr, query.TypeString},
	"email":           {"e.employee_email", query.TypeString},
	"department":      {"e.employee_department", query.TypeString},
	"job_title":       {"e.employee_job_title", query.TypeString},
	"created_at":      {"e.employee_created_at", query.TypeDate},
	"updated_at":      {"e.employee_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist. Every key must also appear in
// sortValue (store_search.go) or the next-page cursor is minted from the wrong
// column.
var sortFields = map[string]resolved{
	"name":            {nameExpr, query.TypeString},
	"email":           {"LOWER(e.employee_email)", query.TypeString},
	"document_number": {"COALESCE(e.employee_number,'')", query.TypeString},
	"record_number":   {"COALESCE(e.employee_number,'')", query.TypeString},
	"status":          {"COALESCE(e.employee_status, 0)", query.TypeNumber},
	"created_at":      {"e.employee_created_at", query.TypeDate},
	"updated_at":      {"e.employee_updated_at", query.TypeDate},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

var _ query.SortResolver = resolver{}

// SearchPredicate matches the number, name, email, job title and department.
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"e.employee_number ILIKE '%'||" + ph + "||'%'" +
		" OR (e.employee_first_name || ' ' || e.employee_last_name) ILIKE '%'||" + ph + "||'%'" +
		" OR e.employee_email ILIKE '%'||" + ph + "||'%'" +
		" OR e.employee_job_title ILIKE '%'||" + ph + "||'%'" +
		" OR e.employee_department ILIKE '%'||" + ph + "||'%'" +
		")"
}

var _ query.SearchResolver = resolver{}

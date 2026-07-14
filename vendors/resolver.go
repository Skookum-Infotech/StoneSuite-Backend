package vendors

import "stonesuite-backend/query"

// resolver implements query.FieldResolver + SortResolver + SearchResolver
// for vendor (mirrors salesorder.resolver). Table alias `v` matches
// vendorSelect.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist.
var systemFields = map[string]resolved{
	"id":              {"v.vendor_uuid::text", query.TypeString},
	"document_number": {"COALESCE(v.vendor_number,'')", query.TypeString},
	"record_number":   {"COALESCE(v.vendor_number,'')", query.TypeString},
	"vendor_type":     {"v.vendor_type", query.TypeString},
	"status":          {"v.vendor_status::text", query.TypeString},
	"owner_id":        {"v.vendor_owner_id::text", query.TypeString},
	"email":           {"v.vendor_email", query.TypeString},
	"legal_name":      {"v.vendor_legal_name", query.TypeString},
	"given_name":      {"v.vendor_given_name", query.TypeString},
	"family_name":     {"v.vendor_family_name", query.TypeString},
	"created_by":      {"v.vendor_created_by::text", query.TypeString},
	"updated_by":      {"v.vendor_updated_by::text", query.TypeString},
	"created_at":      {"v.vendor_created_at", query.TypeDate},
	"updated_at":      {"v.vendor_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number.
var sortableFields = map[string]resolved{
	"document_number": {"v.vendor_number", query.TypeString},
	"record_number":   {"v.vendor_number", query.TypeString},
	"legal_name":      {"v.vendor_legal_name", query.TypeString},
	"family_name":     {"v.vendor_family_name", query.TypeString},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: vendor number, legal
// name, person name, and email.
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"v.vendor_number ILIKE '%'||" + ph + "||'%'" +
		" OR v.vendor_legal_name ILIKE '%'||" + ph + "||'%'" +
		" OR v.vendor_given_name ILIKE '%'||" + ph + "||'%'" +
		" OR v.vendor_family_name ILIKE '%'||" + ph + "||'%'" +
		" OR v.vendor_email ILIKE '%'||" + ph + "||'%'" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

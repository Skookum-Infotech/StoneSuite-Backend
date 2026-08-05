package requisition

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors purchaseorder.validCustomKey / estimate.validCustomKey
// so JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// requisitions. Table alias `reqn` matches reqnSelect (store_get.go).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist.
var systemFields = map[string]resolved{
	"id":               {"reqn.requisition_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(reqn.requisition_number,'')", query.TypeString},
	"record_number":    {"COALESCE(reqn.requisition_number,'')", query.TypeString},
	"status":           {"reqn.requisition_status::text", query.TypeString},
	"priority":         {"reqn.requisition_priority", query.TypeString},
	"requested_by_id":  {"reqn.requisition_requested_by_id::text", query.TypeString},
	"vendor_id":        {"reqn.requisition_vendor_id::text", query.TypeString},
	"vendor_name":      {"reqn.requisition_vendor_name", query.TypeString},
	"department":       {"reqn.requisition_department", query.TypeString},
	"needed_by_date":   {"reqn.requisition_needed_by_date", query.TypeDate},
	"payment_terms_id": {"reqn.requisition_payment_terms::text", query.TypeString},
	"estimated_total":  {"reqn.requisition_estimated_total", query.TypeNumber},
	"created_by":       {"reqn.requisition_created_by::text", query.TypeString},
	"updated_by":       {"reqn.requisition_updated_by::text", query.TypeString},
	"created_at":       {"reqn.requisition_created_at", query.TypeDate},
	"updated_at":       {"reqn.requisition_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "reqn.requisition_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number. needed_by_date is excluded
// since it is nullable (breaks keyset-cursor comparison).
var sortableFields = map[string]resolved{
	"document_number": {"reqn.requisition_number", query.TypeString},
	"record_number":   {"reqn.requisition_number", query.TypeString},
	"estimated_total": {"reqn.requisition_estimated_total", query.TypeNumber},
	"status":          {"reqn.requisition_status", query.TypeNumber},
	"priority":        {"reqn.requisition_priority", query.TypeString},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: document number,
// vendor snapshot name, memo, department, and item SKU/name (child,
// correlated EXISTS).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"reqn.requisition_number ILIKE '%'||" + ph + "||'%'" +
		" OR reqn.requisition_vendor_name ILIKE '%'||" + ph + "||'%'" +
		" OR reqn.requisition_memo ILIKE '%'||" + ph + "||'%'" +
		" OR reqn.requisition_department ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM requisition_item ri WHERE ri.requisition_id = reqn.requisition_id" +
		"   AND (ri.sku ILIKE '%'||" + ph + "||'%' OR ri.item_name ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

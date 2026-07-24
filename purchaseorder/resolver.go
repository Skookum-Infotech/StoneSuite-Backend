package purchaseorder

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors estimate.validCustomKey / crmstore.validCustomKey
// so JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// purchase orders (spec §4). Table alias `po` matches poSelect (store.go).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist (spec §4).
var systemFields = map[string]resolved{
	"id":               {"po.purchase_order_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(po.purchase_order_number,'')", query.TypeString},
	"record_number":    {"COALESCE(po.purchase_order_number,'')", query.TypeString},
	"vendor_id":        {"po.purchase_order_vendor_id::text", query.TypeString},
	"vendor_name":      {"po.purchase_order_vendor_name", query.TypeString},
	"status":           {"po.purchase_order_status::text", query.TypeString},
	"owner_id":         {"po.purchase_order_owner_id::text", query.TypeString},
	"order_date":       {"po.purchase_order_date", query.TypeDate},
	"expected_date":    {"po.purchase_order_expected_date", query.TypeDate},
	"reference_number": {"po.purchase_order_reference_number", query.TypeString},
	"currency_id":      {"po.purchase_order_currency::text", query.TypeString},
	"payment_terms_id": {"po.purchase_order_payment_terms::text", query.TypeString},
	"grand_total":      {"po.purchase_order_grand_total", query.TypeNumber},
	"created_by":       {"po.purchase_order_created_by::text", query.TypeString},
	"updated_by":       {"po.purchase_order_updated_by::text", query.TypeString},
	"created_at":       {"po.purchase_order_created_at", query.TypeDate},
	"updated_at":       {"po.purchase_order_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "po.purchase_order_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number (spec §4). expected_date is
// excluded since it is nullable (breaks keyset-cursor comparison).
var sortableFields = map[string]resolved{
	"document_number": {"po.purchase_order_number", query.TypeString},
	"record_number":   {"po.purchase_order_number", query.TypeString},
	"order_date":      {"po.purchase_order_date", query.TypeDate},
	"grand_total":     {"po.purchase_order_grand_total", query.TypeNumber},
	"status":          {"po.purchase_order_status", query.TypeNumber},
	"vendor_id":       {"po.purchase_order_vendor_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: document number,
// vendor reference, memo/notes, snapshot vendor name (same-table), item
// SKU/name (child, correlated EXISTS), and the current vendor's number
// (spec §4).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"po.purchase_order_number ILIKE '%'||" + ph + "||'%'" +
		" OR po.purchase_order_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR po.purchase_order_memo ILIKE '%'||" + ph + "||'%'" +
		" OR po.purchase_order_notes ILIKE '%'||" + ph + "||'%'" +
		" OR po.purchase_order_vendor_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM purchase_order_item poi WHERE poi.purchase_order_id = po.purchase_order_id" +
		"   AND (poi.sku ILIKE '%'||" + ph + "||'%' OR poi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM vendor v WHERE v.vendor_id = po.purchase_order_vendor_id" +
		"   AND COALESCE(v.vendor_number,'') ILIKE '%'||" + ph + "||'%')" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

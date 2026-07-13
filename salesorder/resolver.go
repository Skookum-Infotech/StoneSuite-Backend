package salesorder

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors crmstore.validCustomKey / workflow.validFieldKey so
// JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// sales_order (spec §11.3, §11.5, §11.6). Table alias `so` matches orderSelect.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist (spec §11.3 table).
var systemFields = map[string]resolved{
	"id":                   {"so.sales_order_uuid::text", query.TypeString},
	"document_number":      {"COALESCE(so.sales_order_number,'')", query.TypeString},
	"record_number":        {"COALESCE(so.sales_order_number,'')", query.TypeString},
	"customer_id":          {"so.sales_order_customer_id::text", query.TypeString},
	"status":               {"so.sales_order_status::text", query.TypeString},
	"sales_rep_id":         {"so.sales_order_sales_rep_id::text", query.TypeString},
	"owner_id":             {"so.sales_order_owner_id::text", query.TypeString},
	"order_date":           {"so.sales_order_date", query.TypeDate},
	"expected_delivery":    {"so.sales_order_expected_delivery", query.TypeDate},
	"currency_id":          {"so.sales_order_currency::text", query.TypeString},
	"payment_terms_id":     {"so.sales_order_payment_terms::text", query.TypeString},
	"payment_due_date":     {"so.sales_order_payment_due_date", query.TypeDate},
	"price_level_id":       {"so.sales_order_price_level::text", query.TypeString},
	"grand_total":          {"so.sales_order_grand_total", query.TypeNumber},
	"po_number":            {"so.sales_order_po_number", query.TypeString},
	"ship_same_as_billing": {"so.sales_order_ship_same_as_bill", query.TypeBool},
	"created_by":           {"so.sales_order_created_by::text", query.TypeString},
	"updated_by":           {"so.sales_order_updated_by::text", query.TypeString},
	"created_at":           {"so.sales_order_created_at", query.TypeDate},
	"updated_at":           {"so.sales_order_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "so.sales_order_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number (spec §11.6).
var sortableFields = map[string]resolved{
	"document_number": {"so.sales_order_number", query.TypeString},
	"record_number":   {"so.sales_order_number", query.TypeString},
	"order_date":      {"so.sales_order_date", query.TypeDate},
	"grand_total":     {"so.sales_order_grand_total", query.TypeNumber},
	"status":          {"so.sales_order_status", query.TypeNumber},
	"customer_id":     {"so.sales_order_customer_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: document number,
// PO/reference, notes, snapshot customer name (same-table), and item SKU/name
// (child) via a correlated EXISTS, plus the current customer's name/code
// (spec §11.5).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"so.sales_order_number ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_po_number ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_memo ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_notes ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM sales_order_item soi WHERE soi.sales_order_id = so.sales_order_id" +
		"   AND (soi.sku ILIKE '%'||" + ph + "||'%' OR soi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer sc WHERE sc.customer_id = so.sales_order_customer_id" +
		"   AND (sc.customer_name ILIKE '%'||" + ph + "||'%' OR sc.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

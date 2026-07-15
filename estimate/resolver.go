package estimate

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors salesorder.validCustomKey / crmstore.validCustomKey
// so JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// estimate (spec §11). Table alias `est` matches estimateSelect (Task 7).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist (spec §11 table).
var systemFields = map[string]resolved{
	"id":               {"est.estimate_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(est.estimate_number,'')", query.TypeString},
	"record_number":    {"COALESCE(est.estimate_number,'')", query.TypeString},
	"customer_id":      {"est.estimate_customer_id::text", query.TypeString},
	"status":           {"est.estimate_status::text", query.TypeString},
	"sales_rep_id":     {"est.estimate_sales_rep_id::text", query.TypeString},
	"owner_id":         {"est.estimate_owner_id::text", query.TypeString},
	"estimate_date":    {"est.estimate_date", query.TypeDate},
	"valid_until":      {"est.estimate_valid_until", query.TypeDate},
	"currency_id":      {"est.estimate_currency::text", query.TypeString},
	"payment_terms_id": {"est.estimate_payment_terms::text", query.TypeString},
	"price_level_id":   {"est.estimate_price_level::text", query.TypeString},
	"grand_total":      {"est.estimate_grand_total", query.TypeNumber},
	"po_number":        {"est.estimate_po_number", query.TypeString},
	"created_by":       {"est.estimate_created_by::text", query.TypeString},
	"updated_by":       {"est.estimate_updated_by::text", query.TypeString},
	"created_at":       {"est.estimate_created_at", query.TypeDate},
	"updated_at":       {"est.estimate_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "est.estimate_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number (spec §11). valid_until is
// excluded since it is nullable (breaks keyset-cursor comparison).
var sortableFields = map[string]resolved{
	"document_number": {"est.estimate_number", query.TypeString},
	"record_number":   {"est.estimate_number", query.TypeString},
	"estimate_date":   {"est.estimate_date", query.TypeDate},
	"grand_total":     {"est.estimate_grand_total", query.TypeNumber},
	"status":          {"est.estimate_status", query.TypeNumber},
	"customer_id":     {"est.estimate_customer_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: document number,
// PO/reference, notes, snapshot customer name (same-table), item SKU/name
// (child, correlated EXISTS), and the current customer's name/code (spec §11).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"est.estimate_number ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_po_number ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_memo ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_notes ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM estimate_item ei WHERE ei.estimate_id = est.estimate_id" +
		"   AND (ei.sku ILIKE '%'||" + ph + "||'%' OR ei.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = est.estimate_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

package creditmemo

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for credit memo. Table alias "cm" = credit_memo.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":               {"cm.credit_memo_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(cm.credit_memo_number,'')", query.TypeString},
	"record_number":    {"COALESCE(cm.credit_memo_number,'')", query.TypeString},
	"customer_id":      {"cm.credit_memo_customer_id::text", query.TypeString},
	"invoice_id":       {"cm.credit_memo_invoice_id::text", query.TypeString},
	"sales_order_id":   {"cm.credit_memo_sales_order_id::text", query.TypeString},
	"status":           {"cm.credit_memo_status::text", query.TypeString},
	"sales_rep_id":     {"cm.credit_memo_sales_rep_id::text", query.TypeString},
	"owner_id":         {"cm.credit_memo_owner_id::text", query.TypeString},
	"credit_memo_date": {"cm.credit_memo_date", query.TypeDate},
	"reason":           {"cm.credit_memo_reason", query.TypeString},
	"reference_number": {"cm.credit_memo_reference_number", query.TypeString},
	"currency_id":      {"cm.credit_memo_currency::text", query.TypeString},
	"price_level_id":   {"cm.credit_memo_price_level::text", query.TypeString},
	"grand_total":      {"cm.credit_memo_grand_total", query.TypeNumber},
	"applied_total":    {"cm.credit_memo_applied_total", query.TypeNumber},
	"unapplied_amount": {"cm.credit_memo_unapplied_amount", query.TypeNumber},
	"created_by":       {"cm.credit_memo_created_by::text", query.TypeString},
	"updated_by":       {"cm.credit_memo_updated_by::text", query.TypeString},
	"created_at":       {"cm.credit_memo_created_at", query.TypeDate},
	"updated_at":       {"cm.credit_memo_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "cm.credit_memo_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist, deliberately narrower than
// systemFields. Nullable columns (credit_memo_invoice_id, sales_order_id,
// owner_id, sales_rep_id) are filterable but NOT sortable: NULLs break
// keyset-cursor comparison (mirrors invoice.sortFields).
var sortFields = map[string]resolved{
	"document_number":  {"COALESCE(cm.credit_memo_number,'')", query.TypeString},
	"record_number":    {"COALESCE(cm.credit_memo_number,'')", query.TypeString},
	"credit_memo_date": {"cm.credit_memo_date", query.TypeDate},
	"grand_total":      {"cm.credit_memo_grand_total", query.TypeNumber},
	"applied_total":    {"cm.credit_memo_applied_total", query.TypeNumber},
	"unapplied_amount": {"cm.credit_memo_unapplied_amount", query.TypeNumber},
	"status":           {"cm.credit_memo_status", query.TypeNumber},
	"customer_id":      {"cm.credit_memo_customer_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

var _ query.SortResolver = resolver{}

func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"cm.credit_memo_number ILIKE '%'||" + ph + "||'%'" +
		" OR cm.credit_memo_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR cm.credit_memo_reason ILIKE '%'||" + ph + "||'%'" +
		" OR cm.credit_memo_memo ILIKE '%'||" + ph + "||'%'" +
		" OR cm.credit_memo_notes ILIKE '%'||" + ph + "||'%'" +
		" OR cm.credit_memo_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM credit_memo_item cmi WHERE cmi.credit_memo_id = cm.credit_memo_id" +
		"   AND (cmi.sku ILIKE '%'||" + ph + "||'%' OR cmi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = cm.credit_memo_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}

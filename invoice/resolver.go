package invoice

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for invoice. Table alias "i" = invoice.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":                   {"i.invoice_uuid::text", query.TypeString},
	"document_number":      {"COALESCE(i.invoice_number,'')", query.TypeString},
	"record_number":        {"COALESCE(i.invoice_number,'')", query.TypeString},
	"customer_id":          {"i.invoice_customer_id::text", query.TypeString},
	"sales_order_id":       {"i.invoice_sales_order_id::text", query.TypeString},
	"status":               {"i.invoice_status::text", query.TypeString},
	"sales_rep_id":         {"i.invoice_sales_rep_id::text", query.TypeString},
	"owner_id":             {"i.invoice_owner_id::text", query.TypeString},
	"invoice_date":         {"i.invoice_date", query.TypeDate},
	"due_date":             {"i.invoice_due_date", query.TypeDate},
	"currency_id":          {"i.invoice_currency::text", query.TypeString},
	"payment_terms_id":     {"i.invoice_payment_terms::text", query.TypeString},
	"price_level_id":       {"i.invoice_price_level::text", query.TypeString},
	"grand_total":          {"i.invoice_grand_total", query.TypeNumber},
	"amount_paid":          {"i.invoice_amount_paid", query.TypeNumber},
	"balance_due":          {"i.invoice_balance_due", query.TypeNumber},
	"po_number":            {"i.invoice_po_number", query.TypeString},
	"ship_same_as_billing": {"i.invoice_ship_same_as_bill", query.TypeBool},
	"created_by":           {"i.invoice_created_by::text", query.TypeString},
	"updated_by":           {"i.invoice_updated_by::text", query.TypeString},
	"created_at":           {"i.invoice_created_at", query.TypeDate},
	"updated_at":           {"i.invoice_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "i.invoice_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist. Nullable columns (e.g. invoice_due_date)
// are deliberately excluded: NULLs break keyset-cursor comparison. due_date stays
// filterable via systemFields, just not sortable (mirrors salesorder.sortableFields).
var sortFields = map[string]resolved{
	"document_number": {"COALESCE(i.invoice_number,'')", query.TypeString},
	"record_number":   {"COALESCE(i.invoice_number,'')", query.TypeString},
	"invoice_date":    {"i.invoice_date", query.TypeDate},
	"grand_total":     {"i.invoice_grand_total", query.TypeNumber},
	"balance_due":     {"i.invoice_balance_due", query.TypeNumber},
	"status":          {"i.invoice_status", query.TypeNumber},
	"customer_id":     {"i.invoice_customer_id", query.TypeNumber},
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
		"i.invoice_number ILIKE '%'||" + ph + "||'%'" +
		" OR i.invoice_po_number ILIKE '%'||" + ph + "||'%'" +
		" OR i.invoice_memo ILIKE '%'||" + ph + "||'%'" +
		" OR i.invoice_notes ILIKE '%'||" + ph + "||'%'" +
		" OR i.invoice_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM invoice_item ii WHERE ii.invoice_id = i.invoice_id" +
		"   AND (ii.sku ILIKE '%'||" + ph + "||'%' OR ii.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = i.invoice_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}

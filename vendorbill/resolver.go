package vendorbill

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for vendor_bill. Table alias "vb" = vendor_bill.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":                    {"vb.vendor_bill_uuid::text", query.TypeString},
	"document_number":       {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"record_number":         {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"vendor_id":             {"vb.vendor_bill_vendor_id::text", query.TypeString},
	"purchase_order_id":     {"vb.vendor_bill_purchase_order_id::text", query.TypeString},
	"status":                {"vb.vendor_bill_status::text", query.TypeString},
	"owner_id":              {"vb.vendor_bill_owner_id::text", query.TypeString},
	"vendor_invoice_number": {"vb.vendor_bill_vendor_invoice_number", query.TypeString},
	"bill_date":             {"vb.vendor_bill_date", query.TypeDate},
	"due_date":              {"vb.vendor_bill_due_date", query.TypeDate},
	"currency_id":           {"vb.vendor_bill_currency::text", query.TypeString},
	"payment_terms_id":      {"vb.vendor_bill_payment_terms::text", query.TypeString},
	"grand_total":           {"vb.vendor_bill_grand_total", query.TypeNumber},
	"amount_paid":           {"vb.vendor_bill_amount_paid", query.TypeNumber},
	"balance_due":           {"vb.vendor_bill_balance_due", query.TypeNumber},
	"created_by":            {"vb.vendor_bill_created_by::text", query.TypeString},
	"updated_by":            {"vb.vendor_bill_updated_by::text", query.TypeString},
	"created_at":            {"vb.vendor_bill_created_at", query.TypeDate},
	"updated_at":            {"vb.vendor_bill_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "vb.vendor_bill_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist. due_date is deliberately excluded
// (nullable columns break keyset comparison) -- it stays filterable via
// systemFields, just not sortable, mirroring invoice.sortFields.
var sortFields = map[string]resolved{
	"document_number": {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"record_number":   {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"bill_date":       {"vb.vendor_bill_date", query.TypeDate},
	"grand_total":     {"vb.vendor_bill_grand_total", query.TypeNumber},
	"balance_due":     {"vb.vendor_bill_balance_due", query.TypeNumber},
	"status":          {"vb.vendor_bill_status", query.TypeNumber},
	"vendor_id":       {"vb.vendor_bill_vendor_id", query.TypeNumber},
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
		"vb.vendor_bill_number ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_vendor_invoice_number ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_memo ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_notes ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_vendor_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM vendor_bill_item vbi WHERE vbi.vendor_bill_id = vb.vendor_bill_id" +
		"   AND (vbi.sku ILIKE '%'||" + ph + "||'%' OR vbi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM vendor v WHERE v.vendor_id = vb.vendor_bill_vendor_id" +
		"   AND (v.vendor_legal_name ILIKE '%'||" + ph + "||'%' OR v.vendor_given_name ILIKE '%'||" + ph + "||'%' OR v.vendor_family_name ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}

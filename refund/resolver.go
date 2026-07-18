package refund

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":               {"rfnd.refund_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(rfnd.refund_number,'')", query.TypeString},
	"record_number":    {"COALESCE(rfnd.refund_number,'')", query.TypeString},
	"customer_id":      {"rfnd.refund_customer_id::text", query.TypeString},
	"status":           {"rfnd.refund_status::text", query.TypeString},
	"method_id":        {"rfnd.refund_method::text", query.TypeString},
	"reference_number": {"rfnd.refund_reference_number", query.TypeString},
	"refund_date":      {"rfnd.refund_date", query.TypeDate},
	"currency_id":      {"rfnd.refund_currency::text", query.TypeString},
	"payment_id":       {"rfnd.refund_payment_id::text", query.TypeString},
	"credit_memo_id":   {"rfnd.refund_credit_memo_id::text", query.TypeString},
	"invoice_id":       {"rfnd.refund_invoice_id::text", query.TypeString},
	"amount":           {"rfnd.refund_amount", query.TypeNumber},
	"applied_total":    {"rfnd.refund_applied_total", query.TypeNumber},
	"unapplied_amount": {"rfnd.refund_unapplied_amount", query.TypeNumber},
	"owner_id":         {"rfnd.refund_owner_id::text", query.TypeString},
	"created_by":       {"rfnd.refund_created_by::text", query.TypeString},
	"updated_by":       {"rfnd.refund_updated_by::text", query.TypeString},
	"created_at":       {"rfnd.refund_created_at", query.TypeDate},
	"updated_at":       {"rfnd.refund_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "rfnd.refund_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

var sortFields = map[string]resolved{
	"document_number":  {"COALESCE(rfnd.refund_number,'')", query.TypeString},
	"record_number":    {"COALESCE(rfnd.refund_number,'')", query.TypeString},
	"refund_date":      {"rfnd.refund_date", query.TypeDate},
	"amount":           {"rfnd.refund_amount", query.TypeNumber},
	"unapplied_amount": {"rfnd.refund_unapplied_amount", query.TypeNumber},
	"status":           {"rfnd.refund_status", query.TypeNumber},
	"customer_id":      {"rfnd.refund_customer_id", query.TypeNumber},
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
		"rfnd.refund_number ILIKE '%'||" + ph + "||'%'" +
		" OR rfnd.refund_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR rfnd.refund_memo ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = rfnd.refund_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}

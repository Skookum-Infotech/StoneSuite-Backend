package payment

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
	"id":                {"p.payment_uuid::text", query.TypeString},
	"document_number":   {"COALESCE(p.payment_number,'')", query.TypeString},
	"record_number":     {"COALESCE(p.payment_number,'')", query.TypeString},
	"customer_id":       {"p.payment_customer_id::text", query.TypeString},
	"status":            {"p.payment_status::text", query.TypeString},
	"method_id":         {"p.payment_method::text", query.TypeString},
	"reference_number":  {"p.payment_reference_number", query.TypeString},
	"payment_date":      {"p.payment_date", query.TypeDate},
	"currency_id":       {"p.payment_currency::text", query.TypeString},
	"amount":            {"p.payment_amount", query.TypeNumber},
	"applied_total":     {"p.payment_applied_total", query.TypeNumber},
	"unapplied_amount":  {"p.payment_unapplied_amount", query.TypeNumber},
	"owner_id":          {"p.payment_owner_id::text", query.TypeString},
	"created_by":        {"p.payment_created_by::text", query.TypeString},
	"updated_by":        {"p.payment_updated_by::text", query.TypeString},
	"created_at":        {"p.payment_created_at", query.TypeDate},
	"updated_at":        {"p.payment_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "p.payment_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

var sortFields = map[string]resolved{
	"document_number":  {"COALESCE(p.payment_number,'')", query.TypeString},
	"record_number":    {"COALESCE(p.payment_number,'')", query.TypeString},
	"payment_date":     {"p.payment_date", query.TypeDate},
	"amount":           {"p.payment_amount", query.TypeNumber},
	"unapplied_amount": {"p.payment_unapplied_amount", query.TypeNumber},
	"status":           {"p.payment_status", query.TypeNumber},
	"customer_id":      {"p.payment_customer_id", query.TypeNumber},
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
		"p.payment_number ILIKE '%'||" + ph + "||'%'" +
		" OR p.payment_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR p.payment_memo ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = p.payment_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}

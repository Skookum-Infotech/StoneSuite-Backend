package vendorbill

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
	"id":               {"vb.vendor_bill_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"record_number":    {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"vendor_id":        {"vb.vendor_bill_vendor_id::text", query.TypeString},
	"status":           {"vb.vendor_bill_status::text", query.TypeString},
	"grand_total":      {"vb.vendor_bill_grand_total", query.TypeNumber},
	"amount_paid":      {"vb.vendor_bill_amount_paid", query.TypeNumber},
	"balance_due":      {"vb.vendor_bill_balance_due", query.TypeNumber},
	"bill_date":        {"vb.vendor_bill_date", query.TypeDate},
	"due_date":         {"vb.vendor_bill_due_date", query.TypeDate},
	"reference_number": {"vb.vendor_bill_reference_number", query.TypeString},
	"owner_id":         {"vb.vendor_bill_owner_id::text", query.TypeString},
	"created_at":       {"vb.vendor_bill_created_at", query.TypeDate},
	"updated_at":       {"vb.vendor_bill_updated_at", query.TypeDate},
}

// Resolve maps a whitelisted logical field key to its SQL expression + data
// type; an unresolved key returns ok=false (mapped to HTTP 400 by query.Build,
// never raw SQL).
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

var sortFields = map[string]resolved{
	"document_number": {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"record_number":   {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"bill_date":       {"vb.vendor_bill_date", query.TypeDate},
	"due_date":        {"vb.vendor_bill_due_date", query.TypeDate},
	"grand_total":     {"vb.vendor_bill_grand_total", query.TypeNumber},
	"balance_due":     {"vb.vendor_bill_balance_due", query.TypeNumber},
	"status":          {"vb.vendor_bill_status", query.TypeNumber},
	"vendor_id":       {"vb.vendor_bill_vendor_id", query.TypeNumber},
}

// SortExpr declares the additional sortable columns beyond the query
// package's built-in created_at/updated_at/record_number.
func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

var _ query.SortResolver = resolver{}

// SearchPredicate emits the ILIKE fragment for the free-text global search
// term bound at placeholder ph.
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"vb.vendor_bill_number ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_memo ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_vendor_name ILIKE '%'||" + ph + "||'%'" +
		")"
}

var _ query.SearchResolver = resolver{}

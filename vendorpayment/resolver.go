package vendorpayment

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
	"id":               {"vp.vendor_payment_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(vp.vendor_payment_number,'')", query.TypeString},
	"record_number":    {"COALESCE(vp.vendor_payment_number,'')", query.TypeString},
	"vendor_id":        {"vp.vendor_payment_vendor_id::text", query.TypeString},
	"status":           {"vp.vendor_payment_status::text", query.TypeString},
	"method_id":        {"vp.vendor_payment_method::text", query.TypeString},
	"reference_number": {"vp.vendor_payment_reference_number", query.TypeString},
	"payment_date":     {"vp.vendor_payment_date", query.TypeDate},
	"scheduled_date":   {"vp.vendor_payment_scheduled_date", query.TypeDate},
	"amount":           {"vp.vendor_payment_amount", query.TypeNumber},
	"applied_total":    {"vp.vendor_payment_applied_total", query.TypeNumber},
	"unapplied_amount": {"vp.vendor_payment_unapplied_amount", query.TypeNumber},
	"approval_status":  {"vp.vendor_payment_approval_status", query.TypeString},
	"owner_id":         {"vp.vendor_payment_owner_id::text", query.TypeString},
	"created_by":       {"vp.vendor_payment_created_by::text", query.TypeString},
	"updated_by":       {"vp.vendor_payment_updated_by::text", query.TypeString},
	"created_at":       {"vp.vendor_payment_created_at", query.TypeDate},
	"updated_at":       {"vp.vendor_payment_updated_at", query.TypeDate},
}

// Resolve maps a whitelisted logical field key to its SQL expression + data
// type; an unresolved key returns ok=false (mapped to HTTP 400 by
// query.Build, never raw SQL).
func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "vp.vendor_payment_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

var sortFields = map[string]resolved{
	"document_number":  {"COALESCE(vp.vendor_payment_number,'')", query.TypeString},
	"record_number":    {"COALESCE(vp.vendor_payment_number,'')", query.TypeString},
	"payment_date":     {"vp.vendor_payment_date", query.TypeDate},
	"amount":           {"vp.vendor_payment_amount", query.TypeNumber},
	"unapplied_amount": {"vp.vendor_payment_unapplied_amount", query.TypeNumber},
	"status":           {"vp.vendor_payment_status", query.TypeNumber},
	"vendor_id":        {"vp.vendor_payment_vendor_id", query.TypeNumber},
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
		"vp.vendor_payment_number ILIKE '%'||" + ph + "||'%'" +
		" OR vp.vendor_payment_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR vp.vendor_payment_memo ILIKE '%'||" + ph + "||'%'" +
		" OR vp.vendor_payment_vendor_name ILIKE '%'||" + ph + "||'%'" +
		")"
}

var _ query.SearchResolver = resolver{}

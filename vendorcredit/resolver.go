package vendorcredit

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for vendor_credit. Table alias "vc" = vendor_credit.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":               {"vc.vendor_credit_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(vc.vendor_credit_number,'')", query.TypeString},
	"record_number":    {"COALESCE(vc.vendor_credit_number,'')", query.TypeString},
	"vendor_id":        {"vc.vendor_credit_vendor_id::text", query.TypeString},
	"status":           {"vc.vendor_credit_status::text", query.TypeString},
	"reference_number": {"vc.vendor_credit_reference_number", query.TypeString},
	"credit_date":      {"vc.vendor_credit_date", query.TypeDate},
	"reason":           {"vc.vendor_credit_reason", query.TypeString},
	"grand_total":      {"vc.vendor_credit_grand_total", query.TypeNumber},
	"applied_total":    {"vc.vendor_credit_applied_total", query.TypeNumber},
	"unapplied_amount": {"vc.vendor_credit_unapplied_amount", query.TypeNumber},
	"owner_id":         {"vc.vendor_credit_owner_id::text", query.TypeString},
	"created_at":       {"vc.vendor_credit_created_at", query.TypeDate},
	"updated_at":       {"vc.vendor_credit_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "vc.vendor_credit_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist.
var sortFields = map[string]resolved{
	"document_number":  {"COALESCE(vc.vendor_credit_number,'')", query.TypeString},
	"record_number":    {"COALESCE(vc.vendor_credit_number,'')", query.TypeString},
	"credit_date":      {"vc.vendor_credit_date", query.TypeDate},
	"grand_total":      {"vc.vendor_credit_grand_total", query.TypeNumber},
	"unapplied_amount": {"vc.vendor_credit_unapplied_amount", query.TypeNumber},
	"status":           {"vc.vendor_credit_status", query.TypeNumber},
	"vendor_id":        {"vc.vendor_credit_vendor_id", query.TypeNumber},
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
		"vc.vendor_credit_number ILIKE '%'||" + ph + "||'%'" +
		" OR vc.vendor_credit_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR vc.vendor_credit_reason ILIKE '%'||" + ph + "||'%'" +
		" OR vc.vendor_credit_memo ILIKE '%'||" + ph + "||'%'" +
		" OR vc.vendor_credit_vendor_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM vendor v WHERE v.vendor_id = vc.vendor_credit_vendor_id" +
		"   AND (v.vendor_legal_name ILIKE '%'||" + ph + "||'%' OR v.vendor_given_name ILIKE '%'||" + ph + "||'%' OR v.vendor_family_name ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}

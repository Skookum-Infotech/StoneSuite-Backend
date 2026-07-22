package fabrication

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors the other modules so JSONB custom keys are safe to
// interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// fabrication_job. Table alias `fj` matches jobSelect.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":                    {"fj.fabrication_job_uuid::text", query.TypeString},
	"document_number":       {"COALESCE(fj.fabrication_job_number,'')", query.TypeString},
	"record_number":         {"COALESCE(fj.fabrication_job_number,'')", query.TypeString},
	"sales_order_id":        {"fj.sales_order_id::text", query.TypeString},
	"customer_id":           {"fj.fabrication_job_customer_id::text", query.TypeString},
	"status":                {"fj.fabrication_job_status::text", query.TypeString},
	"approval_status":       {"fj.job_approval_status", query.TypeString},
	"owner_id":              {"fj.job_owner_id::text", query.TypeString},
	"templater_id":          {"fj.job_templater_id::text", query.TypeString},
	"fabricator_id":         {"fj.job_fabricator_id::text", query.TypeString},
	"template_date":         {"fj.job_template_date", query.TypeDate},
	"promised_install_date": {"fj.job_promised_install_date", query.TypeDate},
	"created_at":            {"fj.fabrication_job_created_at", query.TypeDate},
	"updated_at":            {"fj.fabrication_job_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "fj.job_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields lists keyset-sortable columns. A field belongs here only if
// its cursor value can be produced from the Job DTO by sortValue — status is
// deliberately absent because the DTO carries only the status label/code, not
// the integer fabrication_job_status the ORDER BY would use, so no correct
// cursor value exists for it (this is why the sibling salesorder resolver's
// status sort is latently broken; it is not repeated here).
var sortableFields = map[string]resolved{
	"document_number":       {"fj.fabrication_job_number", query.TypeString},
	"record_number":         {"fj.fabrication_job_number", query.TypeString},
	"promised_install_date": {"fj.job_promised_install_date", query.TypeDate},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the global-search box: job number, site customer name,
// notes, and the originating customer's name.
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"fj.fabrication_job_number ILIKE '%'||" + ph + "||'%'" +
		" OR fj.job_site_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR fj.job_notes ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM customer sc WHERE sc.customer_id = fj.fabrication_job_customer_id" +
		"   AND sc.customer_name ILIKE '%'||" + ph + "||'%')" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

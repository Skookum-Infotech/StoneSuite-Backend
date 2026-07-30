package inventory

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors crmstore.validCustomKey / workflow.validFieldKey so
// JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver (+ SortResolver + SearchResolver)
// for inventory_item. No table alias is used — Search selects from
// inventory_item unqualified, so column names are used directly.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":   {"inventory_item_uuid::text", query.TypeString},
	"sku":  {"inventory_item_sku", query.TypeString},
	"name": {"inventory_item_name", query.TypeString},

	"is_active": {"inventory_item_is_active", query.TypeBool},
	// unit_id and tax_rate_id are cast to text and typed as strings. That is the
	// original convention here and it is kept because narrowing an existing
	// filter's operator set is a breaking change for any caller already using
	// `contains` on them. New id fields below are typed numerically instead.
	"unit_id":     {"inventory_item_unit_id::text", query.TypeString},
	"tax_rate_id": {"inventory_item_tax_rate_id::text", query.TypeString},

	// Stone attributes (spec AD-3). thickness_mm is the field that justifies
	// typed columns over custom_fields JSONB: as TypeNumber it supports
	// gt/gte/lt/lte/between, which a JSONB ->> text extraction cannot express.
	"tracking":             {"inventory_item_tracking", query.TypeString},
	"material_id":          {"inventory_item_material_id", query.TypeNumber},
	"color_id":             {"inventory_item_color_id", query.TypeNumber},
	"finish_id":            {"inventory_item_finish_id", query.TypeNumber},
	"thickness_mm":         {"inventory_item_thickness_mm", query.TypeNumber},
	"origin_country_id":    {"inventory_item_origin_country_id", query.TypeNumber},
	"barcode":              {"inventory_item_barcode", query.TypeString},
	"default_warehouse_id": {"inventory_item_default_warehouse_id", query.TypeNumber},

	"unit_price": {"inventory_item_unit_price", query.TypeNumber},
	"created_at": {"inventory_item_created_at", query.TypeDate},
	"updated_at": {"inventory_item_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "inventory_item_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields declares the stable (NOT NULL) columns a client may sort
// inventory items by, beyond the built-in created_at/updated_at.
//
// Every key here must also be handled by sortValue in store_search.go — a
// sortable field missing there mints the cursor from the wrong column and
// silently skips or repeats rows across pages. Nullable columns are excluded
// on purpose: keyset pagination cannot produce a stable order over NULLs,
// which is why material_id and colour_id are filterable but not sortable.
var sortableFields = map[string]resolved{
	"sku":  {"inventory_item_sku", query.TypeString},
	"name": {"inventory_item_name", query.TypeString},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the item picker's global-search box: SKU or name
// contains the term.
func (resolver) SearchPredicate(ph string) string {
	return "(inventory_item_sku ILIKE '%'||" + ph + "||'%' OR inventory_item_name ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

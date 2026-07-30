package inventory

// resolver_unit.go — the query/ field whitelist for serialized units.

import "stonesuite-backend/query"

// unitResolver implements query.FieldResolver (+ SortResolver + SearchResolver)
// for inventory_slab. Expressions are alias-qualified because unitSelect joins
// item, warehouse, bin, bundle and two self-joins for lineage — an unqualified
// column would be ambiguous.
type unitResolver struct{}

var unitSystemFields = map[string]resolved{
	"id":             {"s.inventory_slab_uuid::text", query.TypeString},
	"serial":         {"s.slab_serial", query.TypeString},
	"kind":           {"s.slab_unit_kind", query.TypeString},
	"status":         {"s.slab_status", query.TypeString},
	"form":           {"s.slab_form", query.TypeString},
	"barcode":        {"s.slab_barcode", query.TypeString},
	"grade":          {"s.slab_grade", query.TypeString},
	"lot":            {"s.slab_lot", query.TypeString},
	"block_id":       {"s.slab_block_id", query.TypeString},
	"bundle_label":   {"s.slab_bundle_id", query.TypeString},
	"item_id":        {"ii.inventory_item_uuid::text", query.TypeString},
	"item_sku":       {"ii.inventory_item_sku", query.TypeString},
	"warehouse_id":   {"s.warehouse_id", query.TypeNumber},
	"bin_id":         {"b.inventory_bin_uuid::text", query.TypeString},
	"bin_path":       {"COALESCE(b.bin_path,'')", query.TypeString},
	"vendor_id":      {"s.slab_vendor_id", query.TypeNumber},
	"finish_id":      {"s.slab_finish_id", query.TypeNumber},
	"usable_remnant": {"s.slab_is_usable_remnant", query.TypeBool},

	// Numeric so the yard can ask for "anything over 20 sq ft" or "3cm stone" —
	// a range query, which is the whole reason these are typed columns.
	"area":         {"s.slab_area", query.TypeNumber},
	"length_mm":    {"s.slab_length_mm", query.TypeNumber},
	"width_mm":     {"s.slab_width_mm", query.TypeNumber},
	"thickness_mm": {"s.slab_thickness_mm", query.TypeNumber},

	"created_at": {"s.slab_created_at", query.TypeDate},
	"updated_at": {"s.slab_updated_at", query.TypeDate},
}

func (unitResolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := unitSystemFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// unitSortableFields is restricted to NOT NULL columns. Nullable ones are
// filterable but not sortable: keyset pagination cannot produce a stable order
// over NULLs, so a cursor minted from one would skip or repeat rows.
var unitSortableFields = map[string]resolved{
	"serial": {"s.slab_serial", query.TypeString},
	"area":   {"s.slab_area", query.TypeNumber},
	"status": {"s.slab_status", query.TypeString},
}

func (unitResolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := unitSortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the yard's scan-or-type box: serial, barcode, lot or
// the item's SKU.
func (unitResolver) SearchPredicate(ph string) string {
	return "(s.slab_serial ILIKE '%'||" + ph + "||'%' OR s.slab_barcode ILIKE '%'||" + ph +
		"||'%' OR s.slab_lot ILIKE '%'||" + ph + "||'%' OR ii.inventory_item_sku ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = unitResolver{}
var _ query.SortResolver = unitResolver{}
var _ query.SearchResolver = unitResolver{}

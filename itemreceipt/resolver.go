package itemreceipt

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors purchaseorder.validCustomKey / crmstore.validCustomKey
// so JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// item receipts. Table alias `ir` matches irSelect (store_get.go).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist. Anything not listed here (and not a
// well-formed cf: key) is a 400 via *query.InvalidFilterError — never raw SQL.
var systemFields = map[string]resolved{
	"id":                    {"ir.item_receipt_uuid::text", query.TypeString},
	"document_number":       {"COALESCE(ir.item_receipt_number,'')", query.TypeString},
	"record_number":         {"COALESCE(ir.item_receipt_number,'')", query.TypeString},
	"purchase_order_id":     {"po.purchase_order_uuid::text", query.TypeString},
	"purchase_order_number": {"COALESCE(po.purchase_order_number,'')", query.TypeString},
	"vendor_id":             {"v.vendor_uuid::text", query.TypeString},
	"vendor_name":           {"ir.item_receipt_vendor_name", query.TypeString},
	"status":                {"ir.item_receipt_status::text", query.TypeString},
	"warehouse_id":          {"ir.warehouse_id::text", query.TypeString},
	"owner_id":              {"ir.item_receipt_owner_id::text", query.TypeString},
	"receipt_date":          {"ir.item_receipt_date", query.TypeDate},
	"packing_slip":          {"ir.item_receipt_packing_slip", query.TypeString},
	"carrier":               {"ir.item_receipt_carrier", query.TypeString},
	"tracking_number":       {"ir.item_receipt_tracking_number", query.TypeString},
	"created_by":            {"ir.item_receipt_created_by::text", query.TypeString},
	"updated_by":            {"ir.item_receipt_updated_by::text", query.TypeString},
	"created_at":            {"ir.item_receipt_created_at", query.TypeDate},
	"updated_at":            {"ir.item_receipt_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "ir.item_receipt_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number.
//
// Two exclusions worth stating, because both are easy to add back by mistake:
//
//   - Nullable columns (item_receipt_posted_at, item_receipt_voided_at): a NULL
//     breaks keyset-cursor comparison and silently drops rows from later pages.
//   - `status`: the sort would have to run on the numeric lkp FK, but the
//     response struct carries only the status name and code, so store_search's
//     sortValue could not mint a correct cursor for it. Every key here MUST
//     have a sortValue case (see TestSortValueCoversEverySortableField) —
//     status stays filterable, which is what callers actually want from it.
var sortableFields = map[string]resolved{
	"document_number": {"ir.item_receipt_number", query.TypeString},
	"record_number":   {"ir.item_receipt_number", query.TypeString},
	"receipt_date":    {"ir.item_receipt_date", query.TypeDate},
	"warehouse_id":    {"ir.warehouse_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: receipt number, the
// shipping paperwork (packing slip / tracking / BOL), the snapshot vendor
// name, the source order's number (already joined), and the line item
// SKU/name (child, correlated EXISTS).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"ir.item_receipt_number ILIKE '%'||" + ph + "||'%'" +
		" OR ir.item_receipt_packing_slip ILIKE '%'||" + ph + "||'%'" +
		" OR ir.item_receipt_tracking_number ILIKE '%'||" + ph + "||'%'" +
		" OR ir.item_receipt_bill_of_lading ILIKE '%'||" + ph + "||'%'" +
		" OR ir.item_receipt_carrier ILIKE '%'||" + ph + "||'%'" +
		" OR ir.item_receipt_vendor_name ILIKE '%'||" + ph + "||'%'" +
		" OR COALESCE(po.purchase_order_number,'') ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM item_receipt_line irl WHERE irl.item_receipt_id = ir.item_receipt_id" +
		"   AND irl.item_deleted_at IS NULL" +
		"   AND (irl.sku ILIKE '%'||" + ph + "||'%' OR irl.item_name ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

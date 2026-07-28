package inventoryadjustment

// resolver.go — the query/ field whitelist for adjustments.

import "stonesuite-backend/query"

type resolved struct {
	expr string
	dt   query.DataType
}

// adjustmentResolver implements query.FieldResolver (+ SortResolver +
// SearchResolver). Expressions are alias-qualified because headerSelect joins
// status, warehouse, reason and employee — an unqualified column would be
// ambiguous.
//
// A key not in this map is a *query.InvalidFilterError, which controllers map
// to 400. The whitelist is the boundary: an unrecognised key can never reach
// the database as an identifier.
type adjustmentResolver struct{}

var adjustmentFields = map[string]resolved{
	"id":           {"a.inventory_adjustment_uuid::text", query.TypeString},
	"number":       {"a.adjustment_number", query.TypeString},
	"status":       {"rs.record_status_code", query.TypeString},
	"warehouse_id": {"a.warehouse_id", query.TypeNumber},
	"reason_id":    {"a.inventory_reason_id", query.TypeNumber},
	"owner_id":     {"a.adjustment_owner_id", query.TypeNumber},
	"date":         {"a.adjustment_date", query.TypeDate},
	"posted_at":    {"a.adjustment_posted_at", query.TypeDate},
	"created_at":   {"a.adjustment_created_at", query.TypeDate},
	"updated_at":   {"a.adjustment_updated_at", query.TypeDate},
}

func (adjustmentResolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := adjustmentFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// adjustmentSortable is restricted to NOT NULL columns. adjustment_number is
// nullable for the instant between insert and numbering, and posted_at is null
// until posting — a keyset cursor over either would skip or repeat rows.
var adjustmentSortable = map[string]resolved{
	"date":   {"a.adjustment_date", query.TypeDate},
	"status": {"rs.record_status_code", query.TypeString},
}

func (adjustmentResolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := adjustmentSortable[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the free-text box: document number, notes, or the
// warehouse it corrects.
func (adjustmentResolver) SearchPredicate(ph string) string {
	return "(COALESCE(a.adjustment_number,'') ILIKE '%'||" + ph + "||'%' OR a.adjustment_notes ILIKE '%'||" + ph +
		"||'%' OR w.warehouse_name ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = adjustmentResolver{}
var _ query.SortResolver = adjustmentResolver{}
var _ query.SearchResolver = adjustmentResolver{}

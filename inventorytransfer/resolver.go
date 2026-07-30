package inventorytransfer

// resolver.go — the query/ field whitelist for transfers.

import "stonesuite-backend/query"

type resolved struct {
	expr string
	dt   query.DataType
}

// transferResolver implements query.FieldResolver (+ SortResolver +
// SearchResolver). A key not in this map is a *query.InvalidFilterError, which
// controllers map to 400 — the whitelist is the boundary, so an unrecognised
// key can never reach the database as an identifier.
type transferResolver struct{}

var transferFields = map[string]resolved{
	"id":                {"t.inventory_transfer_uuid::text", query.TypeString},
	"number":            {"t.transfer_number", query.TypeString},
	"status":            {"rs.record_status_code", query.TypeString},
	"from_warehouse_id": {"t.from_warehouse_id", query.TypeNumber},
	"to_warehouse_id":   {"t.to_warehouse_id", query.TypeNumber},
	"owner_id":          {"t.transfer_owner_id", query.TypeNumber},
	"carrier":           {"t.transfer_carrier", query.TypeString},
	"tracking_number":   {"t.transfer_tracking_number", query.TypeString},
	"date":              {"t.transfer_date", query.TypeDate},
	"expected_date":     {"t.transfer_expected_date", query.TypeDate},
	"shipped_at":        {"t.transfer_shipped_at", query.TypeDate},
	"received_at":       {"t.transfer_received_at", query.TypeDate},
	"created_at":        {"t.transfer_created_at", query.TypeDate},
	"updated_at":        {"t.transfer_updated_at", query.TypeDate},
}

func (transferResolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := transferFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// transferSortable is restricted to NOT NULL columns: shipped_at, received_at
// and transfer_number are all nullable, and a keyset cursor over a nullable
// column skips or repeats rows.
var transferSortable = map[string]resolved{
	"date":   {"t.transfer_date", query.TypeDate},
	"status": {"rs.record_status_code", query.TypeString},
}

func (transferResolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := transferSortable[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the free-text box: document number, carrier tracking,
// or either warehouse name.
func (transferResolver) SearchPredicate(ph string) string {
	return "(COALESCE(t.transfer_number,'') ILIKE '%'||" + ph + "||'%' OR t.transfer_tracking_number ILIKE '%'||" + ph +
		"||'%' OR fw.warehouse_name ILIKE '%'||" + ph + "||'%' OR tw.warehouse_name ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = transferResolver{}
var _ query.SortResolver = transferResolver{}
var _ query.SearchResolver = transferResolver{}

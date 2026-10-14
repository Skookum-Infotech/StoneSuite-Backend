package bintransfer

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for bin_transfer. Table alias "bt" = bin_transfer.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist. "status" is the status row id and
// "status_code" the code (DRFT, PAPV...) a list's status chips filter on, read
// through the rs join every header read carries.
var systemFields = map[string]resolved{
	"id":              {"bt.bin_transfer_uuid::text", query.TypeString},
	"document_number": {"COALESCE(bt.bin_transfer_number,'')", query.TypeString},
	"record_number":   {"COALESCE(bt.bin_transfer_number,'')", query.TypeString},
	"status":          {"bt.bin_transfer_status::text", query.TypeString},
	"status_code":     {"rs.record_status_code", query.TypeString},
	"warehouse_id":    {"bt.warehouse_id::text", query.TypeString},
	"transfer_date":   {"bt.bin_transfer_date", query.TypeDate},
	"notes":           {"bt.bin_transfer_notes", query.TypeString},
	"owner_id":        {"bt.bin_transfer_owner_id::text", query.TypeString},
	"created_at":      {"bt.bin_transfer_created_at", query.TypeDate},
	"updated_at":      {"bt.bin_transfer_updated_at", query.TypeDate},
	"completed_at":    {"bt.bin_transfer_completed_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "bt.bin_transfer_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist. Every key must also appear in
// sortValue (store_search.go) or the next-page cursor is minted from the wrong
// column.
var sortFields = map[string]resolved{
	"document_number": {"COALESCE(bt.bin_transfer_number,'')", query.TypeString},
	"record_number":   {"COALESCE(bt.bin_transfer_number,'')", query.TypeString},
	"transfer_date":   {"bt.bin_transfer_date", query.TypeDate},
	"status":          {"bt.bin_transfer_status", query.TypeNumber},
	"created_at":      {"bt.bin_transfer_created_at", query.TypeDate},
	"updated_at":      {"bt.bin_transfer_updated_at", query.TypeDate},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

var _ query.SortResolver = resolver{}

// SearchPredicate matches the document number, its notes, the location name and
// the serial of any live slab on it -- "which transfer is this slab in?" is the
// first question a yard lead asks.
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"bt.bin_transfer_number ILIKE '%'||" + ph + "||'%'" +
		" OR bt.bin_transfer_notes ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM company_location cl WHERE cl.company_location_id = bt.warehouse_id" +
		"   AND cl.name ILIKE '%'||" + ph + "||'%')" +
		" OR EXISTS (SELECT 1 FROM bin_transfer_line btl" +
		"   JOIN inventory_slab sl ON sl.inventory_slab_id = btl.inventory_slab_id" +
		"   WHERE btl.bin_transfer_id = bt.bin_transfer_id AND btl.line_deleted_at IS NULL" +
		"   AND sl.slab_serial ILIKE '%'||" + ph + "||'%')" +
		")"
}

var _ query.SearchResolver = resolver{}

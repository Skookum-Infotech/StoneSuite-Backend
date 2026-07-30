package cashtransfer

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
	"id":              {"ct.cash_transfer_uuid::text", query.TypeString},
	"document_number": {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"record_number":   {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"status":          {"ct.cash_transfer_status::text", query.TypeString},
	"from_account_id": {"ct.from_account_id::text", query.TypeString},
	"to_account_id":   {"ct.to_account_id::text", query.TypeString},
	"amount":          {"ct.cash_transfer_amount", query.TypeNumber},
	"transfer_date":   {"ct.cash_transfer_date", query.TypeDate},
	"reference":       {"ct.cash_transfer_reference", query.TypeString},
	"owner_id":        {"ct.cash_transfer_owner_id::text", query.TypeString},
	"created_by":      {"ct.cash_transfer_created_by::text", query.TypeString},
	"updated_by":      {"ct.cash_transfer_updated_by::text", query.TypeString},
	"created_at":      {"ct.cash_transfer_created_at", query.TypeDate},
	"updated_at":      {"ct.cash_transfer_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "ct.cash_transfer_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

var sortFields = map[string]resolved{
	"document_number": {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"record_number":   {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"transfer_date":   {"ct.cash_transfer_date", query.TypeDate},
	"amount":          {"ct.cash_transfer_amount", query.TypeNumber},
	"status":          {"ct.cash_transfer_status", query.TypeNumber},
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
		"ct.cash_transfer_number ILIKE '%'||" + ph + "||'%'" +
		" OR ct.cash_transfer_reference ILIKE '%'||" + ph + "||'%'" +
		" OR ct.cash_transfer_notes ILIKE '%'||" + ph + "||'%'" +
		")"
}

var _ query.SearchResolver = resolver{}

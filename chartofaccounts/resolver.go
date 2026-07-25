package chartofaccounts

import "stonesuite-backend/query"

// resolver implements query.FieldResolver (+ SortResolver + SearchResolver)
// for coa_account. Search joins lkp_coa_subcategory as s and lkp_coa_category
// as c, so those columns are referenced with their aliases.
//
// Note there is NO "cf:" escape hatch into the attributes JSONB, unlike the
// inventory resolver: CoA accounts have no user-defined custom fields (AD-9),
// and the column holds encrypted material that must never be filterable.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":               {"a.coa_account_uuid::text", query.TypeString},
	"code":             {"a.coa_account_code", query.TypeString},
	"name":             {"a.coa_account_name", query.TypeString},
	"description":      {"a.coa_account_description", query.TypeString},
	"type":             {"a.coa_account_type", query.TypeEnum},
	"bs_pnl":           {"a.coa_account_bs_pnl", query.TypeEnum},
	"is_postable":      {"a.coa_account_is_postable", query.TypeBool},
	"is_active":        {"a.coa_account_is_active", query.TypeBool},
	"is_visible":       {"a.coa_account_is_visible", query.TypeBool},
	"is_system":        {"a.coa_account_is_system", query.TypeBool},
	"depth":            {"a.coa_account_depth", query.TypeNumber},
	"subcategory_code": {"s.subcategory_code", query.TypeNumber},
	"subcategory_name": {"s.subcategory_name", query.TypeString},
	"category_code":    {"c.category_code", query.TypeNumber},
	"category_name":    {"c.category_name", query.TypeString},
	"created_at":       {"a.coa_account_created_at", query.TypeDate},
	"updated_at":       {"a.coa_account_updated_at", query.TypeDate},
}

// Resolve maps a client-facing field key to its SQL expression and data type.
// There is no "cf:" prefix here (AD-9): unlike inventory, CoA accounts have no
// user-defined custom fields, and the attributes JSONB holds encrypted bank
// account material that must never become filterable.
func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// sortableFields declares stable NOT NULL columns clients may sort by beyond
// the built-in created_at/updated_at.
//
// "code" is this module's record_number equivalent. CLAUDE.md restricts sorts
// to created_at/updated_at/record_number; coa_account has no record_number,
// and coa_account_code satisfies the same underlying requirement -- stable,
// NOT NULL, and unique among live rows -- so keyset cursors stay correct.
// query.SortResolver is the supported extension point for exactly this.
// The expression MUST be alias-qualified: the read query LEFT JOINs
// coa_account a second time as p (the parent), so a bare coa_account_code
// would be ambiguous and Postgres would reject the ORDER BY.
var sortableFields = map[string]resolved{
	"code": {"a.coa_account_code", query.TypeString},
}

// SortExpr maps a client-facing sort key to its alias-qualified SQL
// expression and data type.
func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the account picker's global-search box: code or name
// contains the term.
func (resolver) SearchPredicate(ph string) string {
	return "(a.coa_account_code ILIKE '%'||" + ph + "||'%' OR a.coa_account_name ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}

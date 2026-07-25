package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/query"
)

func TestResolverResolve(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		wantOK bool
		wantDT query.DataType
	}{
		{"code", "code", true, query.TypeString},
		{"name", "name", true, query.TypeString},
		{"type", "type", true, query.TypeEnum},
		{"bs_pnl", "bs_pnl", true, query.TypeEnum},
		{"is_active", "is_active", true, query.TypeBool},
		{"is_visible", "is_visible", true, query.TypeBool},
		{"is_postable", "is_postable", true, query.TypeBool},
		{"is_system", "is_system", true, query.TypeBool},
		{"subcategory_code", "subcategory_code", true, query.TypeNumber},
		{"category_code", "category_code", true, query.TypeNumber},
		{"created_at", "created_at", true, query.TypeDate},
		{"updated_at", "updated_at", true, query.TypeDate},
		{"unknown key is rejected", "balance", false, ""},
		{"sql injection attempt is rejected", "name; DROP TABLE coa_account", false, ""},
		{"custom field prefix is rejected", "cf:budget", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, dt, ok := resolver{}.Resolve(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.NotEmpty(t, expr)
				assert.Equal(t, tt.wantDT, dt)
			}
		})
	}
}

// AD-9: CoA accounts have NO user-defined custom fields, so unlike the
// inventory resolver there is no "cf:" escape hatch into the JSONB column.
func TestResolverRejectsCustomFieldAccess(t *testing.T) {
	for _, key := range []string{"cf:x", "attributes", "attributes->>'accountNumber'"} {
		_, _, ok := resolver{}.Resolve(key)
		assert.False(t, ok, "key %q must not resolve", key)
	}
}

func TestResolverSortExpr(t *testing.T) {
	// coa_account_code is the module's record_number equivalent: stable,
	// NOT NULL, unique among live rows, so keyset pagination stays correct.
	expr, dt, ok := resolver{}.SortExpr("code")
	assert.True(t, ok)
	// Alias-qualified: the read query joins coa_account twice (a and p).
	assert.Equal(t, "a.coa_account_code", expr)
	assert.Equal(t, query.TypeString, dt)

	_, _, ok = resolver{}.SortExpr("attributes")
	assert.False(t, ok)
}

func TestResolverInterfaces(t *testing.T) {
	var _ query.FieldResolver = resolver{}
	var _ query.SortResolver = resolver{}
	var _ query.SearchResolver = resolver{}
}

package mytransactions

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// strp returns a pointer to s, for the nullable user-id argument.
func strp(s string) *string { return &s }

func mustSource(t *testing.T, key string) Source {
	t.Helper()
	s, ok := sourceByKey(key)
	require.Truef(t, ok, "source %q", key)
	return s
}

func TestBranchSQL(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		own          bool
		wantContains []string
		wantAbsent   []string
	}{
		{
			name: "document, all scope",
			key:  "invoice",
			wantContains: []string{
				"'invoice'::text AS type", "t.invoice_uuid::text AS id",
				"FROM invoice t LEFT JOIN lkp_record_status s ON s.record_status_id = t.invoice_status LEFT JOIN customer cu",
				"t.invoice_deleted_at IS NULL",
				"(t.invoice_created_by = $1 OR t.invoice_updated_by = $1)",
				"(t.invoice_grand_total)::float8 AS amount",
			},
			wantAbsent: []string{"t.invoice_owner_id = $1", "audit_logs"},
		},
		{
			name:         "document, own scope narrows on the owner column",
			key:          "invoice",
			own:          true,
			wantContains: []string{"t.invoice_owner_id = $1"},
		},
		{
			name:         "requisition own scope narrows on the requester",
			key:          "requisition",
			own:          true,
			wantContains: []string{"t.requisition_requested_by_id = $1"},
			wantAbsent:   []string{"requisition_owner_id"},
		},
		{
			name:         "inventory has no owner, so own scope cannot narrow",
			key:          "inventory_item",
			own:          true,
			wantContains: []string{"NULL::float8 AS amount", "''::text AS status_name"},
			wantAbsent:   []string{" = $1 AND t.inventory_item_owner"},
		},
		{
			name: "CRM reads updated-by-me from the audit trail and pins the stage",
			key:  "lead",
			wantContains: []string{
				"rt.record_type_code = 'LEAD'",
				"EXISTS (SELECT 1 FROM audit_logs al WHERE al.resource_id = t.customer_uuid::text AND al.actor_user_id = $2::uuid",
				"t.customer_created_by = $1",
			},
		},
		{
			name:         "CRM own scope narrows on the CRM owner",
			key:          "customer",
			own:          true,
			wantContains: []string{"t.customer_crm_owner_user_id = $1", "rt.record_type_code = 'CUST'"},
		},
		{
			name:         "unit status is a plain enum column",
			key:          "inventory_unit",
			wantContains: []string{"INITCAP(t.slab_status)", "t.slab_updated_by = $1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := granted{Source: mustSource(t, tt.key), own: tt.own}.branchSQL()
			for _, want := range tt.wantContains {
				assert.Contains(t, sql, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, sql, absent)
			}
		})
	}
}

func allGranted(t *testing.T, own bool) []granted {
	t.Helper()
	var out []granted
	for _, s := range Sources() {
		out = append(out, granted{Source: s, own: own})
	}
	return out
}

func TestBuildPageSQL(t *testing.T) {
	t.Run("no filters binds the employee, user, limit and offset only", func(t *testing.T) {
		sql, args := buildPageSQL(allGranted(t, false), pageFilter{role: RoleAll, limit: 25, offset: 50}, 7, strp("user-1"))
		assert.Equal(t, []any{7, strp("user-1"), 25, 50}, args)
		assert.Contains(t, sql, "ORDER BY updated_at DESC, type ASC, id ASC LIMIT $3 OFFSET $4")
		assert.Contains(t, sql, "COUNT(*) OVER () AS total", "one scan yields the page and the filtered total")
		assert.NotContains(t, sql, " WHERE created_by_me")
		assert.Equal(t, 26, strings.Count(sql, "UNION ALL")+1, "one branch per source")
	})

	t.Run("role filters", func(t *testing.T) {
		created, _ := buildPageSQL(allGranted(t, false), pageFilter{role: RoleCreated, limit: 5}, 1, nil)
		assert.Contains(t, created, ") mine WHERE created_by_me ORDER BY")
		updated, _ := buildPageSQL(allGranted(t, false), pageFilter{role: RoleUpdated, limit: 5}, 1, nil)
		assert.Contains(t, updated, ") mine WHERE NOT created_by_me ORDER BY")
	})

	t.Run("search term is bound, never interpolated", func(t *testing.T) {
		evil := "x'; DROP TABLE invoice;--"
		sql, args := buildPageSQL(allGranted(t, false), pageFilter{role: RoleAll, search: evil, limit: 5}, 1, nil)
		assert.NotContains(t, sql, "DROP TABLE")
		assert.Contains(t, args, "%"+evil+"%")
		assert.Contains(t, sql, "number ILIKE $3 OR name ILIKE $3 OR account ILIKE $3")
	})

	t.Run("search binds before paging so positions line up", func(t *testing.T) {
		sql, args := buildPageSQL(allGranted(t, false), pageFilter{role: RoleCreated, search: "acme", limit: 10, offset: 20}, 1, nil)
		assert.Equal(t, []any{1, (*string)(nil), "%acme%", 10, 20}, args)
		assert.Contains(t, sql, "LIMIT $4 OFFSET $5")
	})

	t.Run("user id is not bound when no source reads the audit trail", func(t *testing.T) {
		only := []granted{{Source: mustSource(t, "invoice")}}
		sql, args := buildPageSQL(only, pageFilter{role: RoleAll, limit: 5, offset: 0}, 9, strp("user-1"))
		assert.Equal(t, []any{9, 5, 0}, args, "an unreferenced bind parameter is a Postgres error")
		assert.NotContains(t, sql, "$2::uuid")
		assert.Contains(t, sql, "LIMIT $2 OFFSET $3")
	})
}

func TestBuildCountSQL(t *testing.T) {
	sql, args := buildCountSQL(allGranted(t, false), pageFilter{role: RoleUpdated, search: "x", limit: 25, offset: 500}, 4, strp("user-1"))
	assert.Equal(t, []any{4, strp("user-1"), "%x%"}, args, "no paging arguments: it counts every matching row")
	assert.True(t, strings.HasPrefix(sql, "SELECT COUNT(*) FROM ("))
	assert.Contains(t, sql, ") mine WHERE NOT created_by_me AND (number ILIKE $3")
	assert.NotContains(t, sql, "LIMIT")
}

func TestBuildSummarySQL(t *testing.T) {
	since := time.Date(2026, 9, 24, 8, 30, 0, 0, time.UTC)
	sql, args := buildSummarySQL(allGranted(t, false), since, 3, strp("user-1"))
	assert.Equal(t, []any{3, strp("user-1"), "2026-09-24 08:30:00"}, args)
	assert.Contains(t, sql, "COUNT(*) FILTER (WHERE created_by_me)")
	assert.Contains(t, sql, "COUNT(*) FILTER (WHERE NOT created_by_me)")
	assert.Contains(t, sql, "updated_at >= $3::timestamp")
}

func TestGrantFor(t *testing.T) {
	inv := mustSource(t, "invoice")
	tests := []struct {
		name    string
		grants  []authz.Grant
		wantOK  bool
		wantOwn bool
	}{
		{"no grant", nil, false, false},
		{"other resource only", []authz.Grant{{Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll}}, false, false},
		{"write without read", []authz.Grant{{Resource: authz.ResourceInvoice, Action: authz.ActionUpdate, Scope: authz.ScopeAll}}, false, false},
		{"read all", []authz.Grant{{Resource: authz.ResourceInvoice, Action: authz.ActionRead, Scope: authz.ScopeAll}}, true, false},
		{"read own", []authz.Grant{{Resource: authz.ResourceInvoice, Action: authz.ActionRead, Scope: authz.ScopeOwn}}, true, true},
		{"wildcard all", []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}}, true, false},
		{"unrecognised scope fails closed to own", []authz.Grant{{Resource: authz.ResourceInvoice, Action: authz.ActionRead, Scope: authz.Scope("team")}}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, ok := grantFor(inv, tt.grants)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantOwn, g.own)
		})
	}
}

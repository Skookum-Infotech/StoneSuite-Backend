package customertransactions

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/dateonly"
)

var testToday = time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

func mustSource(t *testing.T, key string) Source {
	t.Helper()
	s, ok := sourceByKey(key)
	require.Truef(t, ok, "source %q", key)
	return s
}

func allGranted(t *testing.T, own bool) []granted {
	t.Helper()
	var out []granted
	for _, s := range Sources() {
		out = append(out, granted{Source: s, own: own})
	}
	return out
}

func mustDate(t *testing.T, s string) *dateonly.Date {
	t.Helper()
	d, err := dateonly.Parse(s)
	require.NoError(t, err)
	return &d
}

func TestBranchSQL(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		own          bool
		wantContains []string
		wantAbsent   []string
		wantArgs     int // bound arguments after the branch is built
	}{
		{
			name: "invoice, all scope: customer-scoped, overdue binds today only",
			key:  "invoice",
			wantContains: []string{
				"'invoice'::text AS type", "t.invoice_uuid::text AS id",
				"FROM invoice t LEFT JOIN lkp_record_status s ON s.record_status_id = t.invoice_status",
				"t.invoice_deleted_at IS NULL AND t.invoice_customer_id = $1",
				"(t.invoice_balance_due)::float8 AS balance",
				"t.invoice_due_date < $2::date",
			},
			wantAbsent: []string{"t.invoice_owner_id"},
			wantArgs:   2,
		},
		{
			name:         "invoice, own scope adds the owner predicate",
			key:          "invoice",
			own:          true,
			wantContains: []string{"t.invoice_due_date < $2::date", "t.invoice_owner_id = $3"},
			wantArgs:     3,
		},
		{
			name:         "quote cannot be overdue and has no balance",
			key:          "quote",
			wantContains: []string{"NULL::float8 AS balance", "FALSE AS is_overdue", "(t.quote_valid_until)::date AS due_date"},
			wantAbsent:   []string{"$2"},
			wantArgs:     1,
		},
		{
			name:         "installation job has no amount and dates from creation",
			key:          "fabrication_job",
			own:          true,
			wantContains: []string{"NULL::float8 AS amount", "(t.fabrication_job_created_at::date)::date AS doc_date", "t.job_owner_id = $2"},
			wantArgs:     2,
		},
		{
			name:         "payment balance is the clamped unapplied remainder",
			key:          "payment",
			wantContains: []string{"GREATEST(t.payment_unapplied_amount - t.payment_refunded_total - t.payment_credited_total, 0)"},
			wantArgs:     1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBinder(7, 3, testToday)
			sql := granted{Source: mustSource(t, tt.key), own: tt.own}.branchSQL(b)
			for _, want := range tt.wantContains {
				assert.Contains(t, sql, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, sql, absent)
			}
			assert.Len(t, b.args, tt.wantArgs)
		})
	}
}

func TestBuildPageSQL(t *testing.T) {
	t.Run("no filters binds customer, today, limit and offset only", func(t *testing.T) {
		sql, args := buildPageSQL(allGranted(t, false), pageFilter{limit: 25, offset: 50}, 7, 3, testToday)
		assert.Equal(t, []any{7, dateonly.New(testToday), 25, 50}, args, "all scope never binds the employee")
		assert.Contains(t, sql, "ORDER BY doc_date DESC, created_at DESC, type ASC, id ASC LIMIT $3 OFFSET $4")
		assert.Contains(t, sql, "COUNT(*) OVER () AS total")
		assert.Equal(t, 8, strings.Count(sql, "UNION ALL")+1, "one branch per source")
		assert.NotContains(t, sql, ") x WHERE")
	})

	t.Run("own scope binds the employee once for every branch", func(t *testing.T) {
		sql, args := buildPageSQL(allGranted(t, true), pageFilter{limit: 5}, 7, 3, testToday)
		assert.Equal(t, []any{7, 3, dateonly.New(testToday), 5, 0}, args)
		assert.Equal(t, 8, strings.Count(sql, "_owner_id = $2"), "one owner predicate per source, all sharing $2")
	})

	t.Run("today is not bound when invoices are not in the union", func(t *testing.T) {
		only := []granted{{Source: mustSource(t, "quote")}}
		sql, args := buildPageSQL(only, pageFilter{limit: 5}, 9, 0, testToday)
		assert.Equal(t, []any{9, 5, 0}, args, "an unreferenced bind parameter is a Postgres error")
		assert.Contains(t, sql, "LIMIT $2 OFFSET $3")
	})

	t.Run("filters are bound in order, ANDed, never interpolated", func(t *testing.T) {
		evil := "x'; DROP TABLE invoice;--"
		f := pageFilter{
			status: "ZZZZ", from: mustDate(t, "2026-01-01"), to: mustDate(t, "2026-10-31"),
			search: evil, openOnly: true, limit: 10, offset: 20,
		}
		sql, args := buildPageSQL(allGranted(t, false), f, 1, 1, testToday)
		assert.Equal(t, []any{1, dateonly.New(testToday), "ZZZZ", *f.from, *f.to, "%" + evil + "%", 10, 20}, args)
		assert.Contains(t, sql, ") x WHERE status_code = $3 AND doc_date >= $4::date AND doc_date <= $5::date"+
			" AND number ILIKE $6 AND is_open ORDER BY")
		assert.Contains(t, sql, "LIMIT $7 OFFSET $8")
		for _, literal := range []string{"DROP TABLE", "2026-01-01", "'ZZZZ'"} {
			assert.NotContains(t, sql, literal, "client values are never literals")
		}
	})
}

func TestBuildCountSQL(t *testing.T) {
	f := pageFilter{search: "inv", limit: 25, offset: 500}
	sql, args := buildCountSQL(allGranted(t, false), f, 4, 1, testToday)
	assert.Equal(t, []any{4, dateonly.New(testToday), "%inv%"}, args, "no paging arguments: it counts every matching row")
	assert.True(t, strings.HasPrefix(sql, "SELECT COUNT(*) FROM ("))
	assert.Contains(t, sql, ") x WHERE number ILIKE $3")
	assert.NotContains(t, sql, "LIMIT")
}

func TestGrantFor(t *testing.T) {
	inv := mustSource(t, "invoice")
	grant := func(res authz.Resource, a authz.Action, sc authz.Scope) []authz.Grant {
		return []authz.Grant{{Resource: res, Action: a, Scope: sc}}
	}
	tests := []struct {
		name    string
		grants  []authz.Grant
		wantOK  bool
		wantOwn bool
	}{
		{"missing grant", nil, false, false},
		{"other resource only", grant(authz.ResourceQuote, authz.ActionRead, authz.ScopeAll), false, false},
		{"write without read", grant(authz.ResourceInvoice, authz.ActionUpdate, authz.ScopeAll), false, false},
		{"read all", grant(authz.ResourceInvoice, authz.ActionRead, authz.ScopeAll), true, false},
		{"read own", grant(authz.ResourceInvoice, authz.ActionRead, authz.ScopeOwn), true, true},
		{"wildcard all", grant(authz.ResourceAny, authz.ActionAny, authz.ScopeAll), true, false},
		{"unknown scope fails closed to own", grant(authz.ResourceInvoice, authz.ActionRead, authz.Scope("team")), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, ok := grantFor(inv, tt.grants)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantOwn, g.own)
		})
	}
}

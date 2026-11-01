package vendortransactions

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
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
			name: "vendor bill, all scope: vendor-scoped, overdue binds today only",
			key:  "vendor_bill",
			wantContains: []string{
				"'vendor_bill'::text AS type", "t.vendor_bill_uuid AS id",
				"FROM vendor_bill t LEFT JOIN lkp_record_status s ON s.record_status_id = t.vendor_bill_status",
				"t.vendor_bill_deleted_at IS NULL AND t.vendor_bill_vendor_id = $1",
				"(t.vendor_bill_balance_due)::float8 AS balance",
				"t.vendor_bill_due_date < $2::date",
			},
			wantAbsent: []string{"t.vendor_bill_owner_id"},
			wantArgs:   2,
		},
		{
			name: "vendor bill, own scope: also narrows on the owner",
			key:  "vendor_bill",
			own:  true,
			wantContains: []string{
				"t.vendor_bill_vendor_id = $1", "t.vendor_bill_owner_id = $3",
			},
			wantArgs: 3,
		},
		{
			name: "requisition own scope narrows on the requester",
			key:  "requisition",
			own:  true,
			wantContains: []string{
				"t.requisition_deleted_at IS NULL", "t.requisition_vendor_id = $1",
				"t.requisition_requested_by_id = $2",
				"t.requisition_created_at::date",
			},
			wantAbsent: []string{"t.requisition_owner_id", "$3"},
			wantArgs:   2,
		},
		{
			name: "item receipt carries no money",
			key:  "item_receipt",
			wantContains: []string{
				"NULL::float8 AS amount", "NULL::float8 AS balance", "NULL::date AS due_date",
				"t.item_receipt_vendor_id = $1",
			},
			wantArgs: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBinder(7, 42, testToday)
			sql := granted{Source: mustSource(t, tt.key), own: tt.own}.branchSQL(b)
			for _, want := range tt.wantContains {
				assert.Contains(t, sql, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, sql, absent)
			}
			assert.Len(t, b.args, tt.wantArgs)
			assert.Equal(t, 7, b.args[0], "vendor id is always $1")
		})
	}
}

func TestBranchSQL_EveryBranchFiltersVendorAndDeleted(t *testing.T) {
	for _, g := range allGranted(t, true) {
		sql := g.branchSQL(newBinder(7, 42, testToday))
		assert.Contains(t, sql, "t."+g.DeletedAt+" IS NULL", g.Key)
		assert.Contains(t, sql, "t."+g.VendorCol+" = $1", g.Key)
		assert.Contains(t, sql, "t."+g.Owner+" = $", g.Key)
	}
}

func TestWhereSQL(t *testing.T) {
	cur := &cursor{createdAt: time.Date(2026, 10, 1, 8, 30, 0, 500000000, time.UTC), id: testUUID}
	tests := []struct {
		name     string
		f        pageFilter
		want     string
		wantArgs []any
	}{
		{name: "no filters", f: pageFilter{}, want: ""},
		{name: "status", f: pageFilter{status: "APPV"}, want: " WHERE status_code = $2", wantArgs: []any{"APPV"}},
		{name: "open only", f: pageFilter{openOnly: true}, want: " WHERE is_open"},
		{name: "search is bound with wildcards", f: pageFilter{search: "VBIL-1"}, want: " WHERE number ILIKE $2", wantArgs: []any{"%VBIL-1%"}},
		{name: "date range", f: pageFilter{from: mustDate(t, "2026-01-01"), to: mustDate(t, "2026-01-31")},
			want: " WHERE doc_date >= $2::date AND doc_date <= $3::date"},
		{name: "cursor is a row-wise keyset predicate", f: pageFilter{after: cur},
			want:     " WHERE (created_at, id) < ($2::timestamp, $3::uuid)",
			wantArgs: []any{"2026-10-01 08:30:00.5", testUUID}},
		{name: "everything is ANDed, cursor last", f: pageFilter{status: "PART", openOnly: true, search: "x", after: cur},
			want: " WHERE status_code = $2 AND number ILIKE $3 AND is_open AND (created_at, id) < ($4::timestamp, $5::uuid)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBinder(7, 42, testToday)
			got := whereSQL(tt.f, b)
			assert.Equal(t, tt.want, got)
			if tt.wantArgs != nil {
				assert.Equal(t, tt.wantArgs, b.args[1:1+len(tt.wantArgs)])
			}
			for _, v := range []string{"APPV", "PART", testUUID} {
				assert.NotContains(t, got, v, "filter values are bound, never interpolated")
			}
		})
	}
}

func TestBuildPageSQL(t *testing.T) {
	sources := allGranted(t, false)

	t.Run("orders by the keyset and asks for one extra row", func(t *testing.T) {
		sql, args := buildPageSQL(sources, pageFilter{limit: 25}, 7, 42, testToday)
		assert.Contains(t, sql, "ORDER BY created_at DESC, id DESC LIMIT $")
		assert.Equal(t, 26, args[len(args)-1], "limit+1 so the caller can detect a next page")
		assert.Equal(t, 7, args[0])
		assert.Equal(t, len(sources), strings.Count(sql, "UNION ALL")+1, "one branch per readable type")
	})

	t.Run("cursor and limit are the last bound values", func(t *testing.T) {
		f := pageFilter{limit: 10, after: &cursor{createdAt: testToday, id: testUUID}}
		sql, args := buildPageSQL(sources[:1], f, 7, 42, testToday)
		assert.Contains(t, sql, "(created_at, id) < (")
		assert.Equal(t, 11, args[len(args)-1])
		assert.Equal(t, testUUID, args[len(args)-2])
	})
}

// TestOpenAndOverdueDefinitions pins the accounting conventions the open and
// overdue flags follow, so a status change cannot silently shift the totals.
func TestOpenAndOverdueDefinitions(t *testing.T) {
	tests := []struct {
		key      string
		contains []string
		absent   []string
	}{
		// vendorbill.PayableStatuses with money still owed.
		{"vendor_bill", []string{"IN ('APPV','PART','ODUE')", "t.vendor_bill_balance_due > 0"}, []string{"'DRFT'", "'PAPV'", "'PAID'", "'VOID'"}},
		// vendorpayment.applyTx refuses only VOID.
		{"vendor_payment", []string{"s.record_status_code <> 'VOID' AND t.vendor_payment_unapplied_amount > 0"}, []string{"'APPV'", "'SENT'"}},
		// vendorcredit/apply.go: APPV is usable, APPL is fully applied.
		{"vendor_credit", []string{"s.record_status_code = 'APPV' AND t.vendor_credit_unapplied_amount > 0"}, []string{"'APPL'", "'VOID'"}},
		{"purchase_order", []string{"NOT s.record_status_code IN ('CLSD','CANC')"}, nil},
		{"item_receipt", []string{"IN ('PEND','PART')"}, []string{"'RCVD'", "'VOID'"}},
		{"requisition", []string{"IN ('DRFT','PAPV','APPV')"}, []string{"'CANC'"}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			open := mustSource(t, tt.key).OpenWhere
			for _, c := range tt.contains {
				assert.Contains(t, open, c)
			}
			for _, c := range tt.absent {
				assert.NotContains(t, open, c)
			}
		})
	}

	t.Run("a bill flagged ODUE is overdue even without a past due date", func(t *testing.T) {
		sql := granted{Source: mustSource(t, "vendor_bill")}.branchSQL(newBinder(7, 0, testToday))
		assert.Contains(t, sql, "(t.vendor_bill_due_date < $2::date OR s.record_status_code = 'ODUE')")
	})
}

func TestGrantFor(t *testing.T) {
	bill := mustSource(t, "vendor_bill")
	tests := []struct {
		name    string
		grants  []authz.Grant
		wantOK  bool
		wantOwn bool
	}{
		{"no grant", nil, false, false},
		{"all scope", []authz.Grant{{Resource: bill.Resource, Action: authz.ActionRead, Scope: authz.ScopeAll}}, true, false},
		{"own scope", []authz.Grant{{Resource: bill.Resource, Action: authz.ActionRead, Scope: authz.ScopeOwn}}, true, true},
		{"other resource", []authz.Grant{{Resource: authz.ResourceRequisition, Action: authz.ActionRead, Scope: authz.ScopeAll}}, false, false},
		{"update is not read", []authz.Grant{{Resource: bill.Resource, Action: authz.ActionUpdate, Scope: authz.ScopeAll}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, ok := grantFor(bill, tt.grants)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantOwn, g.own)
		})
	}
}

func TestBuildSummarySQL(t *testing.T) {
	bill := granted{Source: mustSource(t, "vendor_bill")}

	t.Run("payables reuse the list branch's open and overdue flags", func(t *testing.T) {
		sql, args := buildPayablesSQL(bill, 7, 42, testToday)
		assert.Contains(t, sql, "FILTER (WHERE is_open)")
		assert.Contains(t, sql, "FILTER (WHERE is_overdue)")
		assert.Contains(t, sql, "t.vendor_bill_vendor_id = $1")
		assert.Len(t, args, 2)
	})

	t.Run("credits window binds today once and counts approved-or-later payments", func(t *testing.T) {
		subset := []granted{{Source: mustSource(t, "vendor_payment")}, {Source: mustSource(t, "vendor_credit")}}
		sql, args := buildCreditsSQL(subset, 7, 42, testToday)
		assert.Contains(t, sql, "status_code IN ('APPV','SCHD','SENT')")
		assert.Contains(t, sql, "doc_date >= $2::date - 365")
		assert.Len(t, args, 2)
	})

	t.Run("direct bill payments are vendor-scoped, live and windowed", func(t *testing.T) {
		sql, args := buildBillPaymentsSQL(bill, 7, 42, testToday)
		assert.Contains(t, sql, "FROM vendor_bill_payment p JOIN vendor_bill t ON t.vendor_bill_id = p.vendor_bill_id")
		assert.Contains(t, sql, "p.deleted_at IS NULL")
		assert.Contains(t, sql, "t.vendor_bill_deleted_at IS NULL")
		assert.Contains(t, sql, "t.vendor_bill_vendor_id = $1")
		assert.Contains(t, sql, "p.paid_at >= $2::date - 365")
		assert.NotContains(t, sql, "owner")
		assert.Len(t, args, 2)

		own := bill
		own.own = true
		sql, args = buildBillPaymentsSQL(own, 7, 42, testToday)
		assert.Contains(t, sql, "t.vendor_bill_owner_id = $2")
		assert.Contains(t, sql, "p.paid_at >= $3::date - 365")
		assert.Equal(t, []any{7, 42}, args[:2])
	})

	t.Run("counts group by type and status", func(t *testing.T) {
		sql, _ := buildCountsSQL(allGranted(t, false), 7, 42, testToday)
		assert.Contains(t, sql, "GROUP BY type, status_code, status_name")
	})
}

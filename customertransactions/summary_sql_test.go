package customertransactions

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/dateonly"
)

func TestBuildReceivablesSQL(t *testing.T) {
	sql, args := buildReceivablesSQL(granted{Source: mustSource(t, "invoice")}, 7, 3, testToday)
	assert.Equal(t, []any{7, dateonly.New(testToday)}, args, "today binds once for overdue and the 12-month window")
	assert.Contains(t, sql, "FILTER (WHERE is_open)")
	assert.Contains(t, sql, "FILTER (WHERE is_overdue)")
	assert.Contains(t, sql, "status_code IN ('SENT','PART','ODUE','PAID') AND doc_date >= $2::date - 365")
}

func TestBuildCreditsSQL(t *testing.T) {
	both := []granted{{Source: mustSource(t, "payment")}, {Source: mustSource(t, "credit_memo")}}
	sql, args := buildCreditsSQL(both, 7, 3, testToday)
	assert.Equal(t, []any{7, dateonly.New(testToday)}, args)
	assert.Contains(t, sql, "type = 'payment' AND status_code IN ('APPV','DEPO') AND doc_date >= $2::date - 365")
	assert.Contains(t, sql, "'credit_memo'::text AS type")
}

func TestBuildCountsSQL(t *testing.T) {
	sql, args := buildCountsSQL(allGranted(t, true), 7, 3, testToday)
	assert.Equal(t, []any{7, 3, dateonly.New(testToday)}, args)
	assert.Contains(t, sql, "GROUP BY type, status_code, status_name")
}

func TestFoldCounts(t *testing.T) {
	sources := []granted{{Source: mustSource(t, "quote")}, {Source: mustSource(t, "invoice")}, {Source: mustSource(t, "refund")}}
	byType := map[string]*Count{
		"invoice": {Type: "invoice", Count: 2, OpenCount: 1, Statuses: []StatusCount{{Code: "SENT", Count: 1}, {Code: "PAID", Count: 1}}},
		"quote":   {Type: "quote", Count: 1, Statuses: []StatusCount{{Code: "DRFT", Count: 1}}},
	}
	got := foldCounts(sources, byType)
	assert.Equal(t, []string{"quote", "invoice"}, []string{got[0].Type, got[1].Type}, "registry order, empty types omitted")
	assert.Len(t, got, 2)
	assert.Equal(t, "PAID", got[1].Statuses[0].Code, "statuses sorted by code")
}

func TestScopeLimited(t *testing.T) {
	tests := []struct {
		name string
		sc   scope
		want bool
	}{
		{"every type, all scope", scope{sources: allGranted(t, false)}, false},
		{"every type, own scope", scope{sources: allGranted(t, true)}, true},
		{"a type unreadable", scope{sources: allGranted(t, false)[1:]}, true},
		{"nothing readable", scope{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.sc.limited())
		})
	}
}

package vendortransactions

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/mytransactions"
)

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestSources_WellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Sources() {
		t.Run(s.Key, func(t *testing.T) {
			assert.Regexp(t, identRe, s.Key, "key is embedded in SQL as a literal")
			assert.False(t, seen[s.Key], "duplicate key")
			seen[s.Key] = true

			assert.NotEmpty(t, s.Label)
			assert.NotEmpty(t, s.Resource)
			assert.Equal(t, "purchases", s.Domain)
			assert.NotEmpty(t, s.Module)
			assert.Regexp(t, identRe, s.Table)
			for name, col := range map[string]string{
				"VendorCol": s.VendorCol, "StatusCol": s.StatusCol, "DeletedAt": s.DeletedAt,
				"CreatedAt": s.CreatedAt, "UpdatedAt": s.UpdatedAt, "Owner": s.Owner,
			} {
				assert.Regexp(t, identRe, col, "%s must be a bare column", name)
			}
			for name, expr := range map[string]string{"ID": s.ID, "Number": s.Number, "Date": s.Date, "OpenWhere": s.OpenWhere} {
				assert.NotEmpty(t, expr, "%s is required", name)
			}
			for name, expr := range map[string]string{
				"ID": s.ID, "Number": s.Number, "Date": s.Date, "DueDate": s.DueDate,
				"Amount": s.Amount, "Balance": s.Balance, "OpenWhere": s.OpenWhere,
			} {
				assert.NotContains(t, expr, ";", "%s must be a single static expression", name)
			}
			if s.Overdue {
				assert.NotEmpty(t, s.DueDate, "an overdue-capable type needs a due date")
			}
		})
	}
	assert.Len(t, Sources(), 6)
}

func TestSources_OnlyVendorBillCanBeOverdue(t *testing.T) {
	for _, s := range Sources() {
		assert.Equal(t, s.Key == keyVendorBill, s.Overdue, s.Key)
	}
}

// TestSources_MatchMyTransactionsPurchasing pins this registry's keys, RBAC
// resources and routes to the vendor-facing documents My Transactions lists, so
// a renamed or newly added purchasing module cannot be forgotten. The vendor
// directory itself and expenses (no vendor column) are not vendor transactions.
func TestSources_MatchMyTransactionsPurchasing(t *testing.T) {
	type id struct{ key, resource, module string }
	notVendorDocs := map[string]bool{"vendor": true, "expense": true}
	want := map[id]bool{}
	for _, s := range mytransactions.Sources() {
		if s.Domain == "purchases" && !notVendorDocs[s.Key] {
			want[id{s.Key, string(s.Resource), s.Module}] = true
		}
	}
	require.NotEmpty(t, want)
	got := map[id]bool{}
	for _, s := range Sources() {
		got[id{s.Key, string(s.Resource), s.Module}] = true
	}
	assert.Equal(t, want, got)
}

func TestSourceByKey(t *testing.T) {
	tests := []struct {
		key string
		ok  bool
	}{
		{"vendor_bill", true},
		{"requisition", true},
		{"expense", false},
		{"vendor", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			s, ok := sourceByKey(tt.key)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.key, s.Key)
			}
		})
	}
}

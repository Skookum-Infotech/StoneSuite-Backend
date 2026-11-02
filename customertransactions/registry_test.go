package customertransactions

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
			assert.Equal(t, "sales", s.Domain)
			assert.NotEmpty(t, s.Module)
			assert.Regexp(t, identRe, s.Table)
			for name, col := range map[string]string{
				"CustomerCol": s.CustomerCol, "StatusCol": s.StatusCol, "DeletedAt": s.DeletedAt,
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
	assert.Len(t, Sources(), 8)
}

func TestSources_OnlyInvoiceCanBeOverdue(t *testing.T) {
	for _, s := range Sources() {
		assert.Equal(t, s.Key == "invoice", s.Overdue, s.Key)
	}
}

// TestSources_MatchMyTransactionsSales pins this registry to the customer-facing
// documents My Transactions lists, so a new sales module cannot be forgotten.
func TestSources_MatchMyTransactionsSales(t *testing.T) {
	type id struct{ key, resource, module string }
	want := map[id]bool{}
	for _, s := range mytransactions.Sources() {
		if s.Domain == "sales" {
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
	s, ok := sourceByKey("invoice")
	require.True(t, ok)
	assert.Equal(t, "invoice", s.Key)
	_, ok = sourceByKey("lead")
	assert.False(t, ok)
}

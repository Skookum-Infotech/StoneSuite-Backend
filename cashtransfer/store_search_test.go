package cashtransfer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Every key in sortFields (plus the query engine's built-in created_at/
// updated_at) MUST have a matching case in sortValue, or the keyset cursor
// for that sort is silently minted from CreatedAt instead of the column the
// query actually ORDERs BY — which corrupts page 2 onward and is invisible
// to any single-page test. Mirrors itemreceipt's
// TestSortValueCoversEverySortableField (itemreceipt/resolver_test.go),
// the reference pattern this module's AD-6 cites for Post/Reverse/Transition
// shape.
func TestSortValueCoversEverySortableField(t *testing.T) {
	probe := CashTransfer{
		Number:       "CTRF-000007",
		TransferDate: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
		Amount:       1234.56,
		CreatedAt:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	meta := ctMeta{statusID: 99}

	for field := range sortFields {
		t.Run(field, func(t *testing.T) {
			got := sortValue(probe, meta, field)
			assert.NotEqual(t, probe.CreatedAt, got,
				"sortValue has no case for %q — it fell through to created_at, "+
					"so the keyset cursor would be minted from the wrong column", field)
		})
	}

	// The engine's own built-ins must resolve too.
	assert.Equal(t, probe.UpdatedAt, sortValue(probe, meta, "updated_at"))
	assert.Equal(t, probe.CreatedAt, sortValue(probe, meta, "created_at"))

	// status is sortable here (unlike itemreceipt's) because ctMeta.statusID
	// carries the numeric FK the ORDER BY actually runs on.
	assert.Equal(t, meta.statusID, sortValue(probe, meta, "status"))
}

// Every field SortExpr accepts must also be accepted by Resolve — the filter
// whitelist is a superset of the sort whitelist, since query.Build resolves
// filters via Resolve and sorts via SortExpr independently.
func TestSortableFieldsAreAlsoFilterable(t *testing.T) {
	r := resolver{}
	for field := range sortFields {
		t.Run(field, func(t *testing.T) {
			_, _, ok := r.Resolve(field)
			assert.True(t, ok, "sortable field %q must also be filterable via Resolve", field)
		})
	}
}

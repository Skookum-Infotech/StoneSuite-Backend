package requisition

import (
	"testing"
	"time"
)

// Every key in sortableFields must be handled by sortValue. A missing case
// falls through to the created_at default, so the keyset cursor is minted from
// a different column than the query ordered by — page 1 looks right and every
// later page is wrong. The bug is invisible to a single-page test, so this
// guards the correspondence directly.
func TestSortValueCoversEverySortableField(t *testing.T) {
	sentinel := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	probe := Requisition{
		Number:         "REQN-000001",
		EstimatedTotal: 9.99,
		Priority:       "high",
		UpdatedAt:      time.Date(2020, 5, 5, 0, 0, 0, 0, time.UTC),
		CreatedAt:      sentinel,
	}
	meta := reqnMeta{statusID: 7}

	for key := range sortableFields {
		got := sortValue(probe, meta, key)
		if v, ok := got.(time.Time); ok && v.Equal(sentinel) {
			t.Errorf("sortValue(%q) fell through to created_at — the cursor would be minted from the wrong column", key)
		}
	}
}

package expense

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
	probe := Expense{
		Number:    "EXPN-000001",
		Total:     9.99,
		CreatedAt: sentinel,
	}
	meta := expMeta{statusID: 7}

	for key := range sortableFields {
		got := sortValue(probe, meta, key)
		if v, ok := got.(time.Time); ok && v.Equal(sentinel) {
			t.Errorf("sortValue(%q) fell through to created_at — the cursor would be minted from the wrong column", key)
		}
	}
}

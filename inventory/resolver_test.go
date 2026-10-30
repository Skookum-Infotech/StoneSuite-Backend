package inventory

import (
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_StockFilters(t *testing.T) {
	for _, tc := range []struct {
		key  string
		expr string
		dt   query.DataType
	}{
		{"tracking", "inventory_item_tracking", query.TypeString},
		{"track_stock", "inventory_item_track_stock", query.TypeBool},
	} {
		t.Run(tc.key, func(t *testing.T) {
			expr, dt, ok := resolver{}.Resolve(tc.key)
			if !ok || expr != tc.expr || dt != tc.dt {
				t.Fatalf("Resolve(%q) = (%q, %v, %v), want (%q, %v, true)", tc.key, expr, dt, ok, tc.expr, tc.dt)
			}
		})
	}
}

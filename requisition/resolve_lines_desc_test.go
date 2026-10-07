package requisition

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalogRow scans preset values, in order, into resolveInventoryItem's
// destinations — enough to stand in for one inventory_item lookup.
type catalogRow struct{ vals []any }

func (r catalogRow) Scan(dest ...any) error {
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.vals[i]))
	}
	return nil
}

// catalogQuerier is a workflow.Querier whose QueryRow always returns row.
type catalogQuerier struct{ row catalogRow }

func (q catalogQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (q catalogQuerier) QueryRow(context.Context, string, ...any) pgx.Row        { return q.row }
func (q catalogQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// TestResolveLinesCatalogDescription covers a catalog-linked line keeping a
// non-blank caller description instead of the inventory item's, while SKU and
// name still snapshot from the catalog.
func TestResolveLinesCatalogDescription(t *testing.T) {
	const (
		catalogSKU  = "SKU-1"
		catalogName = "Catalog Name"
		catalogDesc = "Catalog description"
	)
	q := catalogQuerier{row: catalogRow{vals: []any{
		7, catalogSKU, catalogName, catalogDesc, (*int)(nil), "ea", 12.5,
	}}}

	cases := []struct {
		name     string
		desc     string
		wantDesc string
	}{
		{"blank caller description falls back to catalog", "", catalogDesc},
		{"whitespace-only caller description falls back to catalog", "   ", catalogDesc},
		{"caller description wins", "Extracted from PDF", "Extracted from PDF"},
		{"caller description is trimmed", "  Extracted from PDF  ", "Extracted from PDF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []LineInput{{LineNumber: 1, InventoryItemUUID: "item-uuid", Description: tc.desc, Quantity: 1}}
			got, err := resolveLines(context.Background(), q, items)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantDesc, got[0].desc)
			assert.Equal(t, catalogSKU, got[0].sku)
			assert.Equal(t, catalogName, got[0].name)
		})
	}
}

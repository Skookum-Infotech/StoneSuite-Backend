package salesorder

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// itemQuerier answers the catalog-item lookup with one fixed item.
type itemQuerier struct{ desc string }

func (q itemQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (q itemQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (q itemQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return itemRow(q) }

type itemRow itemQuerier

func (r itemRow) Scan(dest ...any) error {
	*dest[0].(*int) = 7
	*dest[1].(*string) = "SKU-1"
	*dest[2].(*string) = "Taj Mahal"
	*dest[3].(*string) = r.desc
	*dest[5].(*string) = "SQFT"
	*dest[6].(*float64) = 1500
	return nil
}

// A catalog line keeps the item's identity and unit, but a description sent
// with it (e.g. carried from a customer's PO) wins over the catalog's.
func TestResolveLinesCatalogDescription(t *testing.T) {
	cases := []struct {
		name, catalog, sent, want string
	}{
		{"sent description wins", "", "Black Galaxy granite slab", "Black Galaxy granite slab"},
		{"sent description wins over catalog text", "Quartzite", "Calacatta quartz slab", "Calacatta quartz slab"},
		{"blank falls back to catalog", "Quartzite", "  ", "Quartzite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []LineInput2{{LineNumber: 1, InventoryItemUUID: "0ceb974f-0913-4ccb-b055-244337515623", Description: tc.sent, Quantity: 1, UnitPrice: 10}}
			got, err := resolveLines(context.Background(), itemQuerier{desc: tc.catalog}, items, 0)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].desc)
			assert.Equal(t, "Taj Mahal", got[0].name, "identity stays the item's")
			assert.Equal(t, "SQFT", got[0].unitCode, "unit stays the item's")
		})
	}
}

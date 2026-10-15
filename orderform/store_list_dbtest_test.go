//go:build dbtest

package orderform

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/query"
)

func placeOrder(t *testing.T, pool *pgxpool.Pool, who Submitter, area string) *Result {
	t.Helper()
	res, err := Create(context.Background(), pool, who, placeable(area), time.Now().UTC())
	require.NoError(t, err)
	return res
}

func TestList_ShowsOnlyTheCallersOrders_NewestFirst_WithAreas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	acme, borealis := seedPortalCustomer(t, pool), seedPortalCustomer(t, pool)
	first := placeOrder(t, pool, acme, uniq("Kitchen Annex"))
	second := placeOrder(t, pool, acme, uniq("Garage Bar"))
	other := placeOrder(t, pool, borealis, uniq("Deck Bar"))

	page, err := List(ctx, pool, acme.CustomerID, query.Request{Limit: 50})

	require.NoError(t, err)
	require.Len(t, page.Orders, 2)
	assert.Equal(t, second.Form.ID, page.Orders[0].ID, "newest first")
	assert.Equal(t, first.Form.ID, page.Orders[1].ID)
	for _, o := range page.Orders {
		assert.NotEqual(t, other.Form.ID, o.ID, "SECURITY: another customer's order appeared")
		assert.Equal(t, StatusSubmitted, o.Status)
		assert.Len(t, o.Areas, 1)
	}
	got := page.Orders[0]
	want := second.Form
	assert.Equal(t, want.Builder, got.Builder)
	assert.Equal(t, want.InstallDate, got.InstallDate)
	assert.Equal(t, want.MeasureDate, got.MeasureDate)
	assert.Equal(t, want.Areas[0].Name, got.Areas[0].Name)
	assert.Equal(t, want.Areas[0].SinkModel, got.Areas[0].SinkModel)
	assert.NotNil(t, got.Areas[0].FaucetSpread, "an empty spread is [], never null")
}

func TestList_PagesByKeysetWithoutRepeatingOrSkipping(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	var placed []string
	for i := 0; i < 3; i++ {
		placed = append(placed, placeOrder(t, pool, who, uniq("Bar")).Form.ID)
	}

	p1, err := List(ctx, pool, who.CustomerID, query.Request{Limit: 2})
	require.NoError(t, err)
	require.Len(t, p1.Orders, 2)
	require.True(t, p1.HasMore)
	require.NotEmpty(t, p1.NextCursor)

	p2, err := List(ctx, pool, who.CustomerID, query.Request{Limit: 2, Cursor: p1.NextCursor})
	require.NoError(t, err)
	require.Len(t, p2.Orders, 1)
	assert.False(t, p2.HasMore)
	assert.Empty(t, p2.NextCursor)

	got := []string{p1.Orders[0].ID, p1.Orders[1].ID, p2.Orders[0].ID}
	assert.Equal(t, []string{placed[2], placed[1], placed[0]}, got)
}

func TestList_AHostileCustomerFilterIsRefused(t *testing.T) {
	pool := testPool(t)
	who := seedPortalCustomer(t, pool)

	_, err := List(context.Background(), pool, who.CustomerID, query.Request{
		Filters: []query.Clause{{Field: "customer_id", Op: query.OpEq, Value: 1}},
	})

	var invalid *query.InvalidFilterError
	assert.ErrorAs(t, err, &invalid)
}

func TestList_EmptyForACustomerWithNoOrders(t *testing.T) {
	pool := testPool(t)
	who := seedPortalCustomer(t, pool)

	page, err := List(context.Background(), pool, who.CustomerID, query.Request{})

	require.NoError(t, err)
	assert.NotNil(t, page.Orders)
	assert.Empty(t, page.Orders)
	assert.False(t, page.HasMore)
}

func TestGet_ReturnsTheCallersOrderWithItsAreas(t *testing.T) {
	pool := testPool(t)
	who := seedPortalCustomer(t, pool)
	placed := placeOrder(t, pool, who, uniq("Wet Bar"))

	got, err := Get(context.Background(), pool, who.CustomerID, placed.Form.ID)

	require.NoError(t, err)
	assert.Equal(t, placed.Form.ID, got.ID)
	assert.Equal(t, placed.Form.Builder, got.Builder)
	require.Len(t, got.Areas, 1)
	assert.Equal(t, placed.Form.Areas[0].Name, got.Areas[0].Name)
	assert.Equal(t, placed.Form.Areas[0].Color, got.Areas[0].Color)
}

func TestGet_NeverReturnsAnotherCustomersOrderOrAMalformedId(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	acme, borealis := seedPortalCustomer(t, pool), seedPortalCustomer(t, pool)
	theirs := placeOrder(t, pool, borealis, uniq("Deck Bar"))

	_, err := Get(ctx, pool, acme.CustomerID, theirs.Form.ID)
	assert.ErrorIs(t, err, ErrNotFound, "SECURITY: another customer's order was readable")

	_, err = Get(ctx, pool, acme.CustomerID, "not-a-uuid")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = Get(ctx, pool, acme.CustomerID, "00000000-0000-4000-8000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)
}

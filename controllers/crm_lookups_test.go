//go:build dbtest

package controllers

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testTenantPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestQueryCurrencyLookupItems_IncludesSymbol confirms the currency lookup
// query returns a populated display symbol for each active currency, not
// just id/code/name -- the frontend needs the real symbol (e.g. "$") to
// render amounts instead of concatenating the currency code onto a
// hardcoded prefix.
func TestQueryCurrencyLookupItems_IncludesSymbol(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	items, err := queryCurrencyLookupItems(ctx, pool)
	require.NoError(t, err)
	require.NotEmpty(t, items)

	var usd *CurrencyLookupItem
	for i := range items {
		assert.NotEmpty(t, items[i].Symbol, "currency %q missing symbol", items[i].Code)
		if items[i].Code == "USD" {
			usd = &items[i]
		}
	}
	require.NotNil(t, usd, "seeded USD currency not found")
	assert.Equal(t, "$", usd.Symbol)
}

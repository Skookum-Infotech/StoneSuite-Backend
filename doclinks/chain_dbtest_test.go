//go:build dbtest

package doclinks

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// TestEdgeSQL_ValidAgainstSchema runs every hop and every stage read against the
// real schema with no matching rows, so a mistyped column or table fails here
// instead of on a customer's detail page.
func TestEdgeSQL_ValidAgainstSchema(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := pgLoader{pool}
	for _, e := range append(append([]edge{}, upEdges...), downEdges...) {
		_, err := l.Hop(ctx, e, []int32{-1})
		require.NoError(t, err, "%s to %s", e.from, e.to)
	}
	for _, st := range allStages {
		_, err := loadStage(ctx, pool, st, []int32{-1}, "", Access{Allowed: true})
		require.NoError(t, err, "stage %s", st.kind)
	}
}

func TestResolve_UnknownRecordIsNotFound(t *testing.T) {
	_, err := Resolve(context.Background(), testPool(t), KindInvoice, "00000000-0000-0000-0000-000000000000", "", Visibility{})
	require.ErrorIs(t, err, ErrNotFound)
}

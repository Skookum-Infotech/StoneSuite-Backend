//go:build dbtest

package inventorycount

import (
	"context"
	"math"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM inventory_count WHERE count_deleted_at IS NULL`).Scan(&n))
	return n
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := Input{WarehouseID: countWarehouse(t, pool)}
	in.IdempotencyKey, in.RequestHash = uniq("icnt-key"), idempotency.HashRequest([]byte("body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	rows := countRows(t, pool)

	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, rows, countRows(t, pool), "replay must not create a second row")
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := Input{WarehouseID: countWarehouse(t, pool)}
	in.IdempotencyKey, in.RequestHash = uniq("icnt-key"), idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, c.RecordVersion)

	stale := c.RecordVersion + 5
	err = Update(ctx, pool, c.ID, Input{WarehouseID: wh, Notes: "n", ExpectedVersion: &stale}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	cur := c.RecordVersion
	require.NoError(t, Update(ctx, pool, c.ID, Input{WarehouseID: wh, Notes: "n", ExpectedVersion: &cur}, 1))
	after, err := Get(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, after.RecordVersion)

	// A client that sends no version keeps last-write-wins.
	require.NoError(t, Update(ctx, pool, c.ID, Input{WarehouseID: wh, Notes: "m"}, 1))
}

func TestRecordCounts_RejectsNaNAndNegative(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	item := seedBulk(t, pool, wh, 10)
	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	require.NoError(t, err)
	frozen, err := Freeze(ctx, pool, c.ID, 1)
	require.NoError(t, err)
	line := lineFor(t, frozen, item)

	for _, q := range []float64{math.NaN(), -1, 1e12} {
		q := q
		_, err := RecordCounts(ctx, pool, c.ID, []CountEntry{{LineID: line.ID, CountedQty: &q}}, 1)
		assert.True(t, IsClientError(err), "qty %v: got %v", q, err)
	}
}

func TestDelete_PostedCountRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	item := seedBulk(t, pool, wh, 10)
	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	require.NoError(t, err)
	frozen, err := Freeze(ctx, pool, c.ID, 1)
	require.NoError(t, err)
	line := lineFor(t, frozen, item)
	qty := 10.0
	_, err = RecordCounts(ctx, pool, c.ID, []CountEntry{{LineID: line.ID, CountedQty: &qty}}, 1)
	require.NoError(t, err)
	for _, to := range []string{StatusInReview, StatusApproved} {
		_, err := Transition(ctx, pool, c.ID, to, "", 1)
		require.NoError(t, err)
	}
	_, err = Post(ctx, pool, c.ID, 1)
	require.NoError(t, err)

	err = Delete(ctx, pool, c.ID, 1)
	assert.True(t, IsClientError(err), "got %v", err)
	still, err := Get(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPosted, still.StatusCode)
}

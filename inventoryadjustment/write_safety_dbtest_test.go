//go:build dbtest

package inventoryadjustment

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func adjustmentRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM inventory_adjustment WHERE adjustment_deleted_at IS NULL`).Scan(&n))
	return n
}

func adjustmentInput(item string, qty float64) Input {
	return Input{WarehouseID: 1, Lines: []LineInput{{InventoryItemID: item, ReasonID: 1, QtyDelta: qty}}}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item, _ := seedBulkItem(t, pool, 100)
	in := adjustmentInput(item, -3)
	in.IdempotencyKey, in.RequestHash = uniq("iadj-key"), idempotency.HashRequest([]byte("body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	rows := adjustmentRows(t, pool)

	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, rows, adjustmentRows(t, pool), "replay must not create a second row")
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item, _ := seedBulkItem(t, pool, 100)
	in := adjustmentInput(item, -3)
	in.IdempotencyKey, in.RequestHash = uniq("iadj-key"), idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item, _ := seedBulkItem(t, pool, 100)
	a, err := Create(ctx, pool, adjustmentInput(item, -3), 1)
	require.NoError(t, err)
	require.Equal(t, 1, a.RecordVersion)

	stale := a.RecordVersion + 5
	in := adjustmentInput(item, -4)
	in.ExpectedVersion = &stale
	assert.ErrorIs(t, Update(ctx, pool, a.ID, in, 1), ErrVersionConflict)

	cur := a.RecordVersion
	in.ExpectedVersion = &cur
	require.NoError(t, Update(ctx, pool, a.ID, in, 1))
	after, err := Get(ctx, pool, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, after.RecordVersion)

	// A client that sends no version keeps last-write-wins.
	in.ExpectedVersion = nil
	require.NoError(t, Update(ctx, pool, a.ID, in, 1))
}

func TestCreate_RejectsOutOfRangeQuantity(t *testing.T) {
	pool := testPool(t)
	item, _ := seedBulkItem(t, pool, 100)
	_, err := Create(context.Background(), pool, adjustmentInput(item, 1e12), 1)
	assert.True(t, IsClientError(err), "got %v", err)
}

func TestDelete_PostedAdjustmentRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item, _ := seedBulkItem(t, pool, 100)
	a, err := Create(ctx, pool, adjustmentInput(item, -3), 1)
	require.NoError(t, err)
	_, err = approveAndPost(t, pool, a.ID)
	require.NoError(t, err)

	err = Delete(ctx, pool, a.ID, 1)
	assert.True(t, IsClientError(err), "got %v", err)
	still, err := Get(ctx, pool, a.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPosted, still.StatusCode)
}

//go:build dbtest

package inventorytransfer

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func transferRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM inventory_transfer WHERE transfer_deleted_at IS NULL`).Scan(&n))
	return n
}

func transferInput(dest int, item string, qty float64) Input {
	return Input{FromWarehouseID: 1, ToWarehouseID: dest, Lines: []LineInput{{InventoryItemID: item, Qty: qty}}}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 40)
	in := transferInput(dest, item, 5)
	in.IdempotencyKey, in.RequestHash = uniq("itrf-key"), idempotency.HashRequest([]byte("body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	rows := transferRows(t, pool)

	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, rows, transferRows(t, pool), "replay must not create a second row")
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 40)
	in := transferInput(dest, item, 5)
	in.IdempotencyKey, in.RequestHash = uniq("itrf-key"), idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 40)
	tr, err := Create(ctx, pool, transferInput(dest, item, 5), 1)
	require.NoError(t, err)
	require.Equal(t, 1, tr.RecordVersion)

	stale := tr.RecordVersion + 5
	in := transferInput(dest, item, 6)
	in.ExpectedVersion = &stale
	assert.ErrorIs(t, Update(ctx, pool, tr.ID, in, 1), ErrVersionConflict)

	cur := tr.RecordVersion
	in.ExpectedVersion = &cur
	require.NoError(t, Update(ctx, pool, tr.ID, in, 1))
	after, err := Get(ctx, pool, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, after.RecordVersion)

	// A client that sends no version keeps last-write-wins.
	in.ExpectedVersion = nil
	require.NoError(t, Update(ctx, pool, tr.ID, in, 1))
}

func TestCreate_RejectsOutOfRangeQuantity(t *testing.T) {
	pool := testPool(t)
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 40)
	_, err := Create(context.Background(), pool, transferInput(dest, item, 1e12), 1)
	assert.True(t, IsClientError(err), "got %v", err)
}

func TestDelete_ShippedAndReceivedTransferRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 40)
	tr, err := Create(ctx, pool, transferInput(dest, item, 5), 1)
	require.NoError(t, err)
	approve(t, pool, tr.ID)
	_, err = Ship(ctx, pool, tr.ID, 1)
	require.NoError(t, err)
	assert.True(t, IsClientError(Delete(ctx, pool, tr.ID, 1)), "in-transit delete must be refused")

	_, err = Receive(ctx, pool, tr.ID, 1)
	require.NoError(t, err)
	assert.True(t, IsClientError(Delete(ctx, pool, tr.ID, 1)), "received delete must be refused")
	still, err := Get(ctx, pool, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusReceived, still.StatusCode)
}

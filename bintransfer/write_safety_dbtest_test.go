// bintransfer/write_safety_dbtest_test.go
//go:build dbtest

package bintransfer

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func binTransferRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM bin_transfer WHERE bin_transfer_deleted_at IS NULL`).Scan(&n))
	return n
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	to := newBin(t, pool, 1)
	slab := newSlab(t, pool, nil)
	in := Input{WarehouseID: 1, Lines: []LineInput{{UnitID: slab.ID, ToBinID: to}}}
	in.IdempotencyKey, in.RequestHash = uniq("btrf-key"), idempotency.HashRequest([]byte("body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	rows := binTransferRows(t, pool)

	// The replay must also bypass the one-open-transfer-per-slab check that a
	// genuine second create would trip over.
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, rows, binTransferRows(t, pool), "replay must not create a second row")
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	to := newBin(t, pool, 1)
	slab := newSlab(t, pool, nil)
	in := Input{WarehouseID: 1, Lines: []LineInput{{UnitID: slab.ID, ToBinID: to}}}
	in.IdempotencyKey, in.RequestHash = uniq("btrf-key"), idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	to := newBin(t, pool, 1)
	slab := newSlab(t, pool, nil)
	bt := draftMoving(t, pool, to, slab)
	in := Input{WarehouseID: 1, Notes: "edited", Lines: []LineInput{{UnitID: slab.ID, ToBinID: to}}}

	stale := bt.RecordVersion + 5
	in.ExpectedVersion = &stale
	_, err := Update(ctx, pool, bt.ID, in, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	cur := bt.RecordVersion
	in.ExpectedVersion = &cur
	after, err := Update(ctx, pool, bt.ID, in, 1)
	require.NoError(t, err)
	assert.Equal(t, bt.RecordVersion+1, after.RecordVersion)

	// A client that sends no version keeps last-write-wins.
	in.ExpectedVersion = nil
	_, err = Update(ctx, pool, bt.ID, in, 1)
	require.NoError(t, err)
}

func TestDelete_CompletedBinTransferRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	to := newBin(t, pool, 1)
	slab := newSlab(t, pool, nil)
	bt := draftMoving(t, pool, to, slab)
	_, err := Complete(ctx, pool, bt.ID, 1)
	require.NoError(t, err)

	err = Delete(ctx, pool, bt.ID, 1)
	assert.True(t, IsClientError(err), "got %v", err)
	still, err := Get(ctx, pool, bt.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, still.StatusCode)
}

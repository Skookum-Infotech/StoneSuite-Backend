//go:build dbtest

package cashtransfer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9870", "IdemFrom", true, true)
	to := seedAccount(t, pool, "9871", "IdemTo", true, true)

	in := CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 12.5,
		IdempotencyKey: "ct-replay-key", RequestHash: idempotency.HashRequest([]byte("body-a"))}
	first, err := Create(ctx, pool, in, 0)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 0)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM cash_transfer WHERE from_account_id = (SELECT coa_account_id FROM coa_account WHERE coa_account_uuid = $1)`,
		from).Scan(&n))
	require.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9872", "ReuseFrom", true, true)
	to := seedAccount(t, pool, "9873", "ReuseTo", true, true)

	in := CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 5,
		IdempotencyKey: "ct-reuse-key", RequestHash: idempotency.HashRequest([]byte("body-a"))}
	_, err := Create(ctx, pool, in, 0)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 0)
	require.True(t, errors.Is(err, idempotency.ErrKeyReuse), "got %v", err)
}

func TestUpdate_StaleExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9874", "EvFrom", true, true)
	to := seedAccount(t, pool, "9875", "EvTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 20}, 0)
	require.NoError(t, err)

	stale := ct.RecordVersion + 7
	_, err = Update(ctx, pool, ct.ID, UpdateInput{
		FromAccountUUID: from, ToAccountUUID: to, Amount: 30, ExpectedVersion: &stale}, 0)
	require.ErrorIs(t, err, ErrVersionConflict)

	cur := ct.RecordVersion
	updated, err := Update(ctx, pool, ct.ID, UpdateInput{
		FromAccountUUID: from, ToAccountUUID: to, Amount: 30, ExpectedVersion: &cur}, 0)
	require.NoError(t, err)
	require.Equal(t, ct.RecordVersion+1, updated.RecordVersion)
}

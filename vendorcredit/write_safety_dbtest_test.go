//go:build dbtest

package vendorcredit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateVendorCreditInput{VendorUUID: seedVendor(t, pool), Amount: 25}
	in.IdempotencyKey = "vc-key-replay-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM request_idempotency WHERE idem_key = $1`, in.IdempotencyKey).Scan(&n))
	assert.Equal(t, 1, n)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor_credit WHERE vendor_credit_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateVendorCreditInput{VendorUUID: seedVendor(t, pool), Amount: 25}
	in.IdempotencyKey = "vc-key-reuse-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestCreate_RejectsBadAmount(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateVendorCreditInput{VendorUUID: seedVendor(t, pool), Amount: 1e14}, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "got %v", err)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vc, err := Create(ctx, pool, CreateVendorCreditInput{VendorUUID: seedVendor(t, pool), Amount: 25}, 1)
	require.NoError(t, err)

	upd := func(v int) UpdateVendorCreditInput {
		return UpdateVendorCreditInput{Memo: "edited", ExpectedVersion: &v}
	}
	_, err = Update(ctx, pool, vc.ID, upd(vc.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, vc.ID, upd(vc.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
}

func TestApplyIdempotent_SameKeyAppliesOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	bill := seedVendorBill(t, pool, vendor, 100, "APPV")
	credit := seedApprovedCredit(t, pool, vendor, 100)
	key := "vc-apply-" + credit.ID
	hash := idempotency.HashRequest([]byte(credit.ID + "\n" + bill + "30"))

	first, err := ApplyIdempotent(ctx, pool, credit.ID, bill, 30, 1, key, hash)
	require.NoError(t, err)
	second, err := ApplyIdempotent(ctx, pool, credit.ID, bill, 30, 1, key, hash)
	require.NoError(t, err)
	assert.Equal(t, 30.0, first.AppliedTotal)
	assert.Equal(t, 30.0, second.AppliedTotal, "a replay must not add the amount again")

	var amt float64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT SUM(application_amount) FROM vendor_credit_application a
		JOIN vendor_credit vc ON vc.vendor_credit_id = a.vendor_credit_id
		WHERE vc.vendor_credit_uuid = $1 AND a.application_deleted_at IS NULL`, credit.ID).Scan(&amt))
	assert.Equal(t, 30.0, amt)

	// Same key, different request: refused.
	_, err = ApplyIdempotent(ctx, pool, credit.ID, bill, 40, 1, key, idempotency.HashRequest([]byte("other")))
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)

	// A fresh key adds to the existing application.
	third, err := ApplyIdempotent(ctx, pool, credit.ID, bill, 20, 1, key+"-2", idempotency.HashRequest([]byte("third")))
	require.NoError(t, err)
	assert.Equal(t, 50.0, third.AppliedTotal)
}

func TestSoftDelete_RefusedWithLiveApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	bill := seedVendorBill(t, pool, vendor, 100, "APPV")
	credit := seedApprovedCredit(t, pool, vendor, 100)
	_, err := Apply(ctx, pool, credit.ID, bill, 10, 1)
	require.NoError(t, err)

	err = SoftDelete(ctx, pool, credit.ID, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "got %v", err)

	_, err = Reverse(ctx, pool, credit.ID, bill, 1)
	require.NoError(t, err)
	require.NoError(t, SoftDelete(ctx, pool, credit.ID, 1))
	assert.ErrorIs(t, SoftDelete(ctx, pool, credit.ID, 1), ErrNotFound)
}

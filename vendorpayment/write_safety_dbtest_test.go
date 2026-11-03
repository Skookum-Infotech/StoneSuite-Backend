//go:build dbtest

package vendorpayment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/vendorbill"
)

func activeMethodID(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT payment_method_id FROM lkp_payment_method
		WHERE payment_method_deleted_at IS NULL AND payment_method_is_active ORDER BY payment_method_id LIMIT 1`).Scan(&id))
	return id
}

// approvedBill creates a vendor bill of the given total for vendor and moves
// it to APPV. The input is decoded from JSON because the bill's header fields
// live in an unexported embedded struct.
func approvedBill(t *testing.T, pool *pgxpool.Pool, vendor string, total float64) string {
	t.Helper()
	ctx := context.Background()
	var in vendorbill.CreateVendorBillInput
	body := fmt.Sprintf(`{"vendorUuid":%q,"items":[{"lineNumber":1,"description":"x","quantity":1,"unitPrice":%v}]}`, vendor, total)
	require.NoError(t, json.Unmarshal([]byte(body), &in))
	bill, err := vendorbill.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	// No approvers are configured, so PAPV auto-skips straight to APPV.
	_, err = vendorbill.Transition(ctx, pool, bill.ID, "PAPV", 1)
	require.NoError(t, err)
	return bill.ID
}

func newPayment(t *testing.T, pool *pgxpool.Pool, vendor string, amount float64) *VendorPayment {
	t.Helper()
	vp, err := Create(context.Background(), pool, CreateVendorPaymentInput{
		VendorUUID: vendor, MethodID: activeMethodID(t, pool), Amount: amount,
	}, 1)
	require.NoError(t, err)
	return vp
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateVendorPaymentInput{VendorUUID: seedVendor(t, pool), MethodID: activeMethodID(t, pool), Amount: 50}
	in.IdempotencyKey = "vp-key-replay-" + in.VendorUUID
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
		`SELECT COUNT(*) FROM vendor_payment WHERE vendor_payment_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateVendorPaymentInput{VendorUUID: seedVendor(t, pool), MethodID: activeMethodID(t, pool), Amount: 50}
	in.IdempotencyKey = "vp-key-reuse-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestCreate_InlineApplicationFailureLeavesNoPayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	ref := "atomic-" + vendor
	_, err := Create(ctx, pool, CreateVendorPaymentInput{
		VendorUUID: vendor, MethodID: activeMethodID(t, pool), Amount: 50, ReferenceNumber: ref,
		Applications: []ApplicationInput{{VendorBillUUID: "00000000-0000-0000-0000-000000000000", Amount: 10}},
	}, 1)
	require.Error(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor_payment WHERE vendor_payment_reference_number = $1`, ref).Scan(&n))
	assert.Equal(t, 0, n, "a failed inline application must roll the header back")
}

func TestCreate_InlineApplicationCommitsWithHeader(t *testing.T) {
	pool := testPool(t)
	vendor := seedVendor(t, pool)
	bill := approvedBill(t, pool, vendor, 100)
	vp, err := Create(context.Background(), pool, CreateVendorPaymentInput{
		VendorUUID: vendor, MethodID: activeMethodID(t, pool), Amount: 60,
		Applications: []ApplicationInput{{VendorBillUUID: bill, Amount: 25}},
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, 25.0, vp.AppliedTotal)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vp := newPayment(t, pool, seedVendor(t, pool), 50)

	upd := func(v int) UpdateVendorPaymentInput {
		return UpdateVendorPaymentInput{MethodID: activeMethodID(t, pool), Memo: "edited", ExpectedVersion: &v}
	}
	_, err := Update(ctx, pool, vp.ID, upd(vp.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, vp.ID, upd(vp.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
}

func TestApplyIdempotent_SameKeyAppliesOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	bill := approvedBill(t, pool, vendor, 100)
	vp := newPayment(t, pool, vendor, 100)
	key := "vp-apply-" + vp.ID
	hash := idempotency.HashRequest([]byte(vp.ID + "\n" + bill + "30"))

	first, err := ApplyIdempotent(ctx, pool, vp.ID, bill, 30, 1, key, hash)
	require.NoError(t, err)
	second, err := ApplyIdempotent(ctx, pool, vp.ID, bill, 30, 1, key, hash)
	require.NoError(t, err)
	assert.Equal(t, 30.0, first.AppliedTotal)
	assert.Equal(t, 30.0, second.AppliedTotal, "a replay must not add the amount again")

	_, err = ApplyIdempotent(ctx, pool, vp.ID, bill, 40, 1, key, idempotency.HashRequest([]byte("other")))
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)

	third, err := ApplyIdempotent(ctx, pool, vp.ID, bill, 20, 1, key+"-2", idempotency.HashRequest([]byte("third")))
	require.NoError(t, err)
	assert.Equal(t, 50.0, third.AppliedTotal)
}

func TestSoftDelete_RefusedWithLiveApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	bill := approvedBill(t, pool, vendor, 100)
	vp := newPayment(t, pool, vendor, 100)
	_, err := Apply(ctx, pool, vp.ID, bill, 10, 1)
	require.NoError(t, err)

	err = SoftDelete(ctx, pool, vp.ID, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "got %v", err)

	_, err = Unapply(ctx, pool, vp.ID, bill, 1)
	require.NoError(t, err)
	require.NoError(t, SoftDelete(ctx, pool, vp.ID, 1))
	assert.ErrorIs(t, SoftDelete(ctx, pool, vp.ID, 1), ErrNotFound)
}

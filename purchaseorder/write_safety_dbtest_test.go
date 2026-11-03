//go:build dbtest

package purchaseorder

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func baseCreateInput(vendor, item string) CreatePurchaseOrderInput {
	return CreatePurchaseOrderInput{
		VendorUUID: vendor,
		purchaseOrderFields: purchaseOrderFields{
			Items: []LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1}},
		},
	}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor, item := seedVendorAndItem(t, pool)
	in := baseCreateInput(vendor, item)
	in.IdempotencyKey = "po-replay-" + vendor
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
		`SELECT COUNT(*) FROM purchase_order WHERE purchase_order_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor, item := seedVendorAndItem(t, pool)
	in := baseCreateInput(vendor, item)
	in.IdempotencyKey = "po-reuse-" + vendor
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor, item := seedVendorAndItem(t, pool)
	po, err := Create(ctx, pool, baseCreateInput(vendor, item), 1)
	require.NoError(t, err)

	upd := func(v int) UpdatePurchaseOrderInput {
		return UpdatePurchaseOrderInput{
			purchaseOrderFields: purchaseOrderFields{
				Memo:  "edited",
				Items: []LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1}},
			},
			ExpectedVersion: &v,
		}
	}
	_, err = Update(ctx, pool, po.ID, upd(po.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, po.ID, upd(po.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
	assert.Equal(t, po.RecordVersion+1, after.RecordVersion)
}

func TestCreate_RejectsBadInput(t *testing.T) {
	pool := testPool(t)
	vendor, item := seedVendorAndItem(t, pool)

	tooMany := baseCreateInput(vendor, item)
	tooMany.Items = make([]LineInput, maxLineItems+1)
	badDate := baseCreateInput(vendor, item)
	badDate.OrderDate, badDate.ExpectedDate = "2026-02-01", "2026-01-01"
	badDiscount := baseCreateInput(vendor, item)
	badDiscount.Items[0].DiscountPercent = 150

	for name, in := range map[string]CreatePurchaseOrderInput{
		"too many lines": tooMany, "expected before order": badDate, "discount over 100": badDiscount,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Create(context.Background(), pool, in, 1)
			var ce ClientError
			assert.True(t, errors.As(err, &ce), "got %v", err)
		})
	}
}

func TestSoftDelete_RefusedWithLiveReceipt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor, item := seedVendorAndItem(t, pool)
	po, err := Create(ctx, pool, baseCreateInput(vendor, item), 1)
	require.NoError(t, err)

	// A pending receipt row is enough: the gate counts live receipts, it does
	// not need the receiving flow (which lives in another package).
	var internalID, vendorID, warehouseID, irctType, pendStatus int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT po.purchase_order_id, po.purchase_order_vendor_id FROM purchase_order po
		WHERE po.purchase_order_uuid = $1`, po.ID).Scan(&internalID, &vendorID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO company_location (name) VALUES ('PR9 test warehouse') RETURNING company_location_id`).Scan(&warehouseID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'IRCT'`).Scan(&irctType))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'PEND'`, irctType).Scan(&pendStatus))
	var irID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO item_receipt (record_type, item_receipt_status, purchase_order_id,
			item_receipt_vendor_id, item_receipt_vendor_name, warehouse_id, item_receipt_date)
		VALUES ($1,$2,$3,$4,'v',$5,CURRENT_DATE) RETURNING item_receipt_id`,
		irctType, pendStatus, internalID, vendorID, warehouseID).Scan(&irID))

	err = SoftDelete(ctx, pool, po.ID, 1)
	assert.ErrorIs(t, err, ErrHasDependents)

	// Once the receipt is gone the order can be deleted.
	_, err = pool.Exec(ctx, `UPDATE item_receipt SET item_receipt_deleted_at = NOW(), item_receipt_deleted_by = 1 WHERE item_receipt_id = $1`, irID)
	require.NoError(t, err)
	assert.NoError(t, SoftDelete(ctx, pool, po.ID, 1))
}

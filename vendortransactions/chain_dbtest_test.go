//go:build dbtest

package vendortransactions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func TestChain_NoDownstreamNotReceived(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "chain0", readAll()...)
	vUUID, vID := seedVendor(t, pool, "Chain0")
	po := seedDoc(t, pool, vID, docSeed{key: "purchase_order", status: "SENT", total: 10})

	ch, err := GetChain(context.Background(), pool, a.identityID, vUUID, po.uuid)
	require.NoError(t, err)
	assert.Equal(t, ChainNotReceived, ch.Status)
	assert.Empty(t, ch.Receipts.Items)
	assert.Empty(t, ch.Bills.Items)
	assert.Empty(t, ch.Payments.Items)
	assert.False(t, ch.BillsHidden)
}

func TestChain_BillsHiddenWithoutGrant(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "chain1", readAll("vendor_bill")...)
	vUUID, vID := seedVendor(t, pool, "Chain1")
	po := seedDoc(t, pool, vID, docSeed{key: "purchase_order", status: "RCVD", total: 10})
	_, err := pool.Exec(context.Background(),
		`UPDATE vendor_bill SET vendor_bill_purchase_order_id = $1 WHERE vendor_bill_id = $2`, po.id,
		seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "PAID", date: "2026-10-01", total: 10}).id)
	require.NoError(t, err)

	ch, err := GetChain(context.Background(), pool, a.identityID, vUUID, po.uuid)
	require.NoError(t, err)
	assert.Empty(t, ch.Bills.Items)
	assert.Equal(t, 1, ch.Bills.Hidden)
	assert.True(t, ch.BillsHidden)
	assert.Equal(t, ChainNotReceived, ch.Status, "no receipts linked, bills hidden: never above received")
}

func TestChain_OtherVendorsPOIsNotFound(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "chain2", readAll()...)
	vUUID, _ := seedVendor(t, pool, "Chain2")
	_, otherID := seedVendor(t, pool, "Chain2Other")
	po := seedDoc(t, pool, otherID, docSeed{key: "purchase_order", status: "SENT", total: 10})

	_, err := GetChain(context.Background(), pool, a.identityID, vUUID, po.uuid)
	assert.ErrorIs(t, err, ErrPurchaseOrderNotFound)

	noPO := seedActor(t, pool, "chain2b", readAll("purchase_order")...)
	_, err = GetChain(context.Background(), pool, noPO.identityID, vUUID, po.uuid)
	assert.ErrorIs(t, err, ErrPurchaseOrderForbidden)
	assert.NotErrorIs(t, err, ErrPurchaseOrderNotFound)
}

func TestChain_OwnScopedReceiptReaderLearnsNothingOfOthers(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "chain3", readAll("item_receipt")...)
	own := seedActor(t, pool, "chain3o", append(readAll("item_receipt"), read(authz.ResourceItemReceipt, authz.ScopeOwn))...)
	vUUID, vID := seedVendor(t, pool, "Chain3")
	po := seedDoc(t, pool, vID, docSeed{key: "purchase_order", status: "CLSD", total: 10, owner: own.employee})
	rcpt := seedDoc(t, pool, vID, docSeed{key: "item_receipt", status: "PEND", total: 0, owner: a.employee})
	_, err := pool.Exec(context.Background(),
		`UPDATE item_receipt SET purchase_order_id = $1 WHERE item_receipt_id = $2`, po.id, rcpt.id)
	require.NoError(t, err)

	ch, err := GetChain(context.Background(), pool, own.identityID, vUUID, po.uuid)
	require.NoError(t, err)
	assert.Empty(t, ch.Receipts.Items)
	assert.Zero(t, ch.Receipts.Hidden, "own-scoped caller is never told how many others' receipts exist")
	assert.NotEqual(t, ChainReceived, ch.Status, "someone else's receipt must not drive the status")
}

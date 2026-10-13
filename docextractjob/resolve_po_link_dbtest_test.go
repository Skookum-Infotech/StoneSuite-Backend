//go:build dbtest

package docextractjob

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLinkedPO(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	short := tag[len(tag)-9:]
	vendor := seedVendor(t, pool, "Poco"+tag, "ACT_")
	other := seedVendor(t, pool, "Elsepo"+tag, "ACT_")
	r := NewResolver(pool, ResolveOptions{})

	// Partly received and partly billed: 10 ordered, 8 received, 3 billed.
	rcvdUUID, rcvdID := seedPurchaseOrder(t, pool, vendor, "PORD-"+short, "", "PART", 25000)
	lineA := seedPOLine(t, pool, rcvdID, 1, "Slab A", 10, 8, 2000)
	seedPOLine(t, pool, rcvdID, 2, "Slab B", 5, 0, 1000)
	_, billID := seedVendorBill(t, pool, vendor, "VBIL-A"+short, "INV-1", "DRFT", 6000)
	seedBillConversion(t, pool, rcvdID, billID, lineA, 3)
	_, voidID := seedVendorBill(t, pool, vendor, "VBIL-V"+short, "INV-2", "VOID", 4000)
	seedBillConversion(t, pool, rcvdID, voidID, lineA, 4) // a VOID bill claims nothing

	t.Run("billable lines carry received minus billed", func(t *testing.T) {
		got, err := r.ResolveLinkedPO(ctx, vendor, "PO # "+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, rcvdUUID, got.UUID)
		assert.True(t, got.Billable)
		assert.Empty(t, got.Reason)
		require.Len(t, got.Lines, 2)
		assert.Equal(t, lineA, got.Lines[0].ID)
		assert.Equal(t, 1, got.Lines[0].LineNumber)
		assert.EqualValues(t, 10000, got.Lines[0].QtyMilli)
		assert.EqualValues(t, 5000, got.Lines[0].BillableQtyMilli)
		assert.EqualValues(t, 2000, got.Lines[0].UnitPriceCents)
		assert.EqualValues(t, 0, got.Lines[1].BillableQtyMilli)
	})
	t.Run("matches the vendor's own reference number", func(t *testing.T) {
		_, id := seedPurchaseOrder(t, pool, vendor, "PORD-R"+short, "QT-"+short, "RCVD", 100)
		seedPOLine(t, pool, id, 1, "x", 1, 1, 100)
		got, err := r.ResolveLinkedPO(ctx, vendor, "qt "+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "PORD-R"+short, got.Number)
		assert.True(t, got.Billable)
	})
	t.Run("not yet received is not billable", func(t *testing.T) {
		_, id := seedPurchaseOrder(t, pool, vendor, "PORD-S"+short, "", "SENT", 100)
		seedPOLine(t, pool, id, 1, "x", 1, 0, 100)
		got, err := r.ResolveLinkedPO(ctx, vendor, "PORD-S"+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.False(t, got.Billable)
		assert.Contains(t, got.Reason, "has not received goods yet")
		assert.NotEmpty(t, got.UUID)
	})
	t.Run("closed with nothing received has nothing to bill", func(t *testing.T) {
		_, id := seedPurchaseOrder(t, pool, vendor, "PORD-C"+short, "", "CLSD", 100)
		seedPOLine(t, pool, id, 1, "x", 1, 0, 100)
		got, err := r.ResolveLinkedPO(ctx, vendor, "PORD-C"+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.False(t, got.Billable)
		assert.Contains(t, got.Reason, "Nothing has been received")
	})
	t.Run("everything billed already", func(t *testing.T) {
		_, id := seedPurchaseOrder(t, pool, vendor, "PORD-F"+short, "", "RCVD", 100)
		line := seedPOLine(t, pool, id, 1, "x", 2, 2, 100)
		_, b := seedVendorBill(t, pool, vendor, "VBIL-F"+short, "INV-F"+short, "APPV", 200)
		seedBillConversion(t, pool, id, b, line, 2)
		got, err := r.ResolveLinkedPO(ctx, vendor, "PORD-F"+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.False(t, got.Billable)
		assert.Contains(t, got.Reason, "already been billed")
		assert.EqualValues(t, 0, got.Lines[0].BillableQtyMilli)
	})
	t.Run("another vendor's order is refused without leaking it", func(t *testing.T) {
		_, id := seedPurchaseOrder(t, pool, other, "PORD-O"+short, "", "RCVD", 100)
		seedPOLine(t, pool, id, 1, "x", 1, 1, 100)
		got, err := r.ResolveLinkedPO(ctx, vendor, "PORD-O"+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.False(t, got.Billable)
		assert.Empty(t, got.UUID)
		assert.Empty(t, got.Lines)
		assert.NotNil(t, got.Lines, "lines are [] not null")
		assert.Contains(t, got.Reason, "is for another vendor")
	})
	t.Run("not found, blank and unresolved vendor give nil", func(t *testing.T) {
		for _, tc := range []struct{ vendor, ref string }{
			{vendor, "PO-99999" + short}, {vendor, ""}, {vendor, "   "}, {"", short}, {"not-a-uuid", short},
		} {
			got, err := r.ResolveLinkedPO(ctx, tc.vendor, tc.ref)
			require.NoError(t, err)
			assert.Nil(t, got, "%+v", tc)
		}
	})
	t.Run("two orders with the same reference are ambiguous", func(t *testing.T) {
		seedPurchaseOrder(t, pool, vendor, "PORD-D1"+short, "DUP-"+short, "RCVD", 100)
		seedPurchaseOrder(t, pool, vendor, "PORD-D2"+short, "DUP-"+short, "RCVD", 100)
		got, err := r.ResolveLinkedPO(ctx, vendor, "DUP-"+short)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.False(t, got.Billable)
		assert.Contains(t, got.Reason, "more than one purchase order")
	})
}

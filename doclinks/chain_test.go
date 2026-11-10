package doclinks

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLoader answers hops from an in-memory table of (from kind, to kind) -> parent -> children.
type fakeLoader struct{ links map[[2]Kind]map[int32][]int32 }

func (f fakeLoader) Hop(_ context.Context, e edge, from []int32) ([]int32, error) {
	var out []int32
	for _, id := range from {
		out = append(out, f.links[[2]Kind{e.from, e.to}][id]...)
	}
	return out, nil
}

func link(m map[[2]Kind]map[int32][]int32, from Kind, f int32, to Kind, t int32) {
	k := [2]Kind{from, to}
	if m[k] == nil {
		m[k] = map[int32][]int32{}
	}
	m[k][f] = append(m[k][f], t)
}

// fullFlow: quote 1 -> order 10 -> job 20, invoices 30/31 -> payment 40 (on 30)
// -> refund 50 (on payment 40); order 10 also has credit memo 60 -> refund 51.
func fullFlow() fakeLoader {
	m := map[[2]Kind]map[int32][]int32{}
	// down edges
	link(m, KindQuote, 1, KindSalesOrder, 10)
	link(m, KindSalesOrder, 10, KindFabrication, 20)
	link(m, KindSalesOrder, 10, KindInvoice, 30)
	link(m, KindSalesOrder, 10, KindInvoice, 31)
	link(m, KindInvoice, 30, KindPayment, 40)
	link(m, KindPayment, 40, KindRefund, 50)
	link(m, KindSalesOrder, 10, KindCreditMemo, 60)
	link(m, KindCreditMemo, 60, KindRefund, 51)
	// up edges
	link(m, KindSalesOrder, 10, KindQuote, 1)
	link(m, KindFabrication, 20, KindSalesOrder, 10)
	link(m, KindInvoice, 30, KindSalesOrder, 10)
	link(m, KindInvoice, 31, KindSalesOrder, 10)
	link(m, KindPayment, 40, KindInvoice, 30)
	link(m, KindRefund, 50, KindPayment, 40)
	link(m, KindCreditMemo, 60, KindSalesOrder, 10)
	link(m, KindRefund, 51, KindCreditMemo, 60)
	return fakeLoader{m}
}

func sorted(ids []int32) []int32 {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func TestWalk_EveryRecordSeesTheWholeFlow(t *testing.T) {
	want := map[Kind][]int32{
		KindQuote: {1}, KindSalesOrder: {10}, KindFabrication: {20},
		KindInvoice: {30, 31}, KindPayment: {40}, KindCreditMemo: {60}, KindRefund: {50, 51},
	}
	starts := []struct {
		name string
		kind Kind
		id   int32
	}{
		{"quote", KindQuote, 1}, {"sales order", KindSalesOrder, 10}, {"fabrication", KindFabrication, 20},
		{"invoice", KindInvoice, 31}, {"payment", KindPayment, 40}, {"refund via payment", KindRefund, 50},
		{"refund via credit memo", KindRefund, 51},
	}
	for _, s := range starts {
		t.Run(s.name, func(t *testing.T) {
			chain, err := Walk(context.Background(), fullFlow(), s.kind, s.id)
			require.NoError(t, err)
			for kind, ids := range want {
				assert.Equal(t, ids, sorted(chain.ids(kind)), "%s ids", kind)
			}
		})
	}
}

func TestWalk_StandaloneInvoiceStaysAlone(t *testing.T) {
	m := map[[2]Kind]map[int32][]int32{}
	link(m, KindInvoice, 5, KindPayment, 6)
	link(m, KindPayment, 6, KindInvoice, 5)
	chain, err := Walk(context.Background(), fakeLoader{m}, KindPayment, 6)
	require.NoError(t, err)
	assert.Equal(t, []int32{5}, chain.ids(KindInvoice))
	assert.Equal(t, []int32{6}, chain.ids(KindPayment))
	assert.Empty(t, chain.ids(KindSalesOrder))
}

func TestWalk_CyclicDataTerminates(t *testing.T) {
	m := map[[2]Kind]map[int32][]int32{}
	link(m, KindInvoice, 1, KindPayment, 2)
	link(m, KindPayment, 2, KindInvoice, 1)
	link(m, KindPayment, 2, KindRefund, 3)
	link(m, KindRefund, 3, KindPayment, 2)
	chain, err := Walk(context.Background(), fakeLoader{m}, KindRefund, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, chain.size())
}

func TestEdges_AllSQLUsesParameterOnly(t *testing.T) {
	for _, e := range append(append([]edge{}, upEdges...), downEdges...) {
		assert.Contains(t, e.sql, "ANY($1)", "%s to %s", e.from, e.to)
		assert.NotContains(t, e.sql, "$2", "%s to %s", e.from, e.to)
	}
}

// purchaseFlow: requisition 1 -> PO 10 -> receipts 20/21, bill 30 -> payment 40.
func purchaseFlow() fakeLoader {
	m := map[[2]Kind]map[int32][]int32{}
	link(m, KindRequisition, 1, KindPurchaseOrd, 10)
	link(m, KindPurchaseOrd, 10, KindItemReceipt, 20)
	link(m, KindPurchaseOrd, 10, KindItemReceipt, 21)
	link(m, KindPurchaseOrd, 10, KindVendorBill, 30)
	link(m, KindVendorBill, 30, KindVendorPay, 40)
	link(m, KindPurchaseOrd, 10, KindRequisition, 1)
	link(m, KindItemReceipt, 20, KindPurchaseOrd, 10)
	link(m, KindItemReceipt, 21, KindPurchaseOrd, 10)
	link(m, KindVendorBill, 30, KindPurchaseOrd, 10)
	link(m, KindVendorPay, 40, KindVendorBill, 30)
	return fakeLoader{m}
}

func TestWalk_PurchaseFlowFromEveryRecord(t *testing.T) {
	want := map[Kind][]int32{
		KindRequisition: {1}, KindPurchaseOrd: {10}, KindItemReceipt: {20, 21}, KindVendorBill: {30}, KindVendorPay: {40},
	}
	for _, s := range []struct {
		kind Kind
		id   int32
	}{{KindRequisition, 1}, {KindPurchaseOrd, 10}, {KindItemReceipt, 21}, {KindVendorBill, 30}, {KindVendorPay, 40}} {
		chain, err := Walk(context.Background(), purchaseFlow(), s.kind, s.id)
		require.NoError(t, err)
		for kind, ids := range want {
			assert.Equal(t, ids, sorted(chain.ids(kind)), "%s from %s", kind, s.kind)
		}
		assert.Empty(t, chain.ids(KindSalesOrder), "purchase flow never touches sales kinds")
	}
}

func TestFlowOf_PicksStagesByKind(t *testing.T) {
	assert.Equal(t, KindRequisition, flowOf(KindVendorPay)[0].kind)
	assert.Equal(t, KindQuote, flowOf(KindRefund)[0].kind)
	assert.Len(t, flowOf(KindInvoice), 6)
	assert.Len(t, flowOf(KindItemReceipt), 5)
}

package vendortransactions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func f64(v float64) *float64 { return &v }

func TestDeriveChainStatus(t *testing.T) {
	open := chainBill{statusCode: "APPV", balanceDue: f64(50)}
	paid := chainBill{statusCode: "PAID", balanceDue: f64(0)}
	zero := chainBill{statusCode: "PART", balanceDue: f64(0)}
	void := chainBill{statusCode: "VOID", balanceDue: f64(50)}
	tests := []struct {
		name string
		in   chainFacts
		want string
	}{
		{"no downstream", chainFacts{poCode: "SENT"}, ChainNotReceived},
		{"closed without receipts", chainFacts{poCode: "CLSD"}, ChainNotReceived},
		{"cancelled", chainFacts{poCode: "CANC"}, ChainNotReceived},
		{"partially received", chainFacts{poCode: "PART", receipts: 1}, ChainPartiallyReceived},
		{"received", chainFacts{poCode: "RCVD", receipts: 1}, ChainReceived},
		{"closed, only others' receipts invisible to own-scoped caller", chainFacts{poCode: "CLSD", receipts: 0}, ChainNotReceived},
		{"closed, receipts hidden by missing grant still count", chainFacts{poCode: "CLSD", receipts: 1}, ChainReceived},
		{"closed with receipts", chainFacts{poCode: "CLSD", receipts: 2}, ChainReceived},
		{"one open bill", chainFacts{poCode: "RCVD", receipts: 1, bills: []chainBill{open}}, ChainBilled},
		{"bill before receipt", chainFacts{poCode: "SENT", bills: []chainBill{open}}, ChainBilled},
		{"all paid", chainFacts{poCode: "RCVD", receipts: 1, bills: []chainBill{paid, zero}}, ChainPaid},
		{"paid and open", chainFacts{poCode: "RCVD", receipts: 1, bills: []chainBill{paid, open}}, ChainBilled},
		{"void ignored for paid", chainFacts{poCode: "RCVD", receipts: 1, bills: []chainBill{paid, void}}, ChainPaid},
		{"only void bills", chainFacts{poCode: "RCVD", receipts: 1, bills: []chainBill{void}}, ChainReceived},
		{"hidden bills cap at received", chainFacts{poCode: "RCVD", receipts: 1, bills: []chainBill{paid}, billsHidden: true}, ChainReceived},
		{"hidden bills cap partial", chainFacts{poCode: "PART", receipts: 1, billsHidden: true}, ChainPartiallyReceived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deriveChainStatus(tt.in))
		})
	}
}

func TestChainSQL_BindsEverythingAndScopes(t *testing.T) {
	for _, key := range []string{keyItemReceipt, keyVendorBill, keyVendorPayment} {
		src, _ := sourceByKey(key)
		sql, args := buildChainItemsSQL(granted{Source: src, own: true}, 7, 3, 11)
		assert.Contains(t, sql, "IN (SELECT", key)
		assert.NotContains(t, sql, "CURRENT_DATE", key)
		assert.Contains(t, sql, "= $1", key)
		assert.Equal(t, 7, args[0], key)
		assert.Contains(t, args, 11, key)

		csql, cargs := buildChainCountSQL(key, 7, 11)
		assert.True(t, strings.Contains(csql, "$2"), key)
		assert.Equal(t, []any{7, 11}, cargs, key)
	}
	po, _ := sourceByKey(keyPurchaseOrder)
	sql, args := buildChainPOSQL(granted{Source: po}, 7, 0, "00000000-0000-0000-0000-000000000001")
	assert.Contains(t, sql, "::uuid")
	assert.Contains(t, args, "00000000-0000-0000-0000-000000000001")
}

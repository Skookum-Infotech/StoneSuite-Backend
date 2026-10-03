package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func pagesOf(rows ...Row) []PageRows { return []PageRows{{Page: 1, Rows: rows}} }

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		rows []Row
		want string
	}{
		{"purchase order title", []Row{trowSplit(750, tcell{50, "PURCHASE ORDER"})}, ClassPurchaseOrder},
		{"invoice title with number", []Row{trowSplit(750, tcell{50, "Invoice # 1234"})}, ClassInvoice},
		{"quotation", []Row{trowSplit(750, tcell{50, "Quotation"})}, ClassQuote},
		{"estimate", []Row{trowSplit(750, tcell{50, "ESTIMATE"})}, ClassEstimate},
		{"credit memo", []Row{trowSplit(750, tcell{50, "Credit Memo"})}, ClassCreditMemo},
		{"invoice to is not a title", []Row{trowSplit(750, tcell{50, "Invoice To: ACME"})}, ""},
		{"bill to is not a bill", []Row{trowSplit(750, tcell{50, "Bill To:"})}, ""},
		{"vendor bill title", []Row{trowSplit(750, tcell{50, "Vendor Bill"})}, ClassVendorBill},
		{"long sentence ignored", []Row{trowSplit(750, tcell{50, "Invoice copies must be sent to accounts payable at once"})}, ""},
		{"no title", []Row{trowSplit(750, tcell{50, "ACME Stone Inc"})}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Classify(pagesOf(tt.rows...)))
		})
	}
	assert.Equal(t, "", Classify(nil))
}

func TestClassMismatch(t *testing.T) {
	tests := []struct {
		target DocType
		class  string
		want   bool
	}{
		{DocTypeSalesOrder, ClassPurchaseOrder, false},
		{DocTypeSalesOrder, "", false},
		{DocTypeSalesOrder, ClassInvoice, true},
		{DocTypeSalesOrder, ClassQuote, true},
		{DocTypeVendorBill, ClassInvoice, false},
		{DocTypeVendorBill, ClassPurchaseOrder, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ClassMismatch(tt.target, tt.class), "%s/%s", tt.target, tt.class)
	}
}

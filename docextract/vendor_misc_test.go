package docextract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract/internal/pdftest"
)

func TestClassify_VendorSideTitles(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"statement", "STATEMENT", ClassStatement},
		{"statement of account", "Statement of Account", ClassStatement},
		{"packing slip", "Packing Slip", ClassPackingSlip},
		{"delivery ticket", "Delivery Ticket", ClassPackingSlip},
		{"bill of lading", "BILL OF LADING", ClassPackingSlip},
		{"pro forma invoice is a quote", "Pro Forma Invoice", ClassQuote},
		{"proforma", "PROFORMA", ClassQuote},
		{"letter-spaced invoice", "I N V O I C E", ClassInvoice},
		{"letter-spaced statement", "S T A T E M E N T", ClassStatement},
		{"spaced letters that are not a title", "A B C D E F", ""},
		{"statement date is not a title", "Statement Date: 06/30/2026", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Classify(pagesOf(trowSplit(750, tcell{50, tt.text}))))
		})
	}
}

func TestClassMismatch_VendorSide(t *testing.T) {
	tests := []struct {
		target DocType
		class  string
		want   bool
	}{
		{DocTypePurchaseOrder, ClassPurchaseOrder, false},
		{DocTypePurchaseOrder, ClassSalesOrder, false},
		{DocTypePurchaseOrder, ClassQuote, false},
		{DocTypePurchaseOrder, ClassOrderForm, false},
		{DocTypePurchaseOrder, ClassInvoice, true},
		{DocTypePurchaseOrder, ClassStatement, true},
		{DocTypePurchaseOrder, ClassCreditMemo, true},
		{DocTypeVendorBill, ClassInvoice, false},
		{DocTypeVendorBill, ClassVendorBill, false},
		{DocTypeVendorBill, ClassStatement, true},
		{DocTypeVendorBill, ClassPackingSlip, true},
		{DocTypeVendorBill, ClassCreditMemo, true},
		{DocTypeVendorBill, ClassQuote, true},
		{DocTypeVendorBill, "", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ClassMismatch(tt.target, tt.class), "%s/%s", tt.target, tt.class)
	}
}

func TestChargeRows(t *testing.T) {
	tests := []struct {
		desc string
		want bool
	}{
		{"Freight", true}, {"Fuel surcharge", true}, {"Delivery fee", true}, {"Misc charge", true},
		{"Less deposit", true}, {"Less deposit received", true}, {"Less payment", true}, {"Payment received - thank you", true},
		{"Deposit", true}, {"Amount paid", true},
		{"Freight forwarding crate", true}, // starts like freight: still a charge row without an item number
		{"Granite slab", false}, {"Lessing tile", false}, {"Payment terminal stand", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, chargeRe.MatchString(tt.desc), tt.desc)
	}
}

func TestDueFromTerms(t *testing.T) {
	inv := Field{Value: "2026-01-31", Source: SourceDocument, Confidence: ConfHigh}
	tests := []struct {
		name    string
		invoice Field
		terms   string
		want    string
		ok      bool
	}{
		{"net 30 crosses a month end", inv, "Net 30", "2026-03-02", true},
		{"net30 glued", inv, "NET30", "2026-03-02", true},
		{"days", inv, "45 days", "2026-03-17", true},
		{"discount terms read the net days", inv, "2% 10 Net 30", "2026-03-02", true},
		{"due on receipt has no days", inv, "Due on receipt", "", false},
		{"no terms", inv, "", "", false},
		{"no invoice date", notFound(), "Net 30", "", false},
		{"absurd days", inv, "Net 999", "", false},
		{"unparseable invoice date", Field{Value: "soon", Source: SourceDocument, Confidence: ConfCheck}, "Net 30", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terms := notFound()
			if tt.terms != "" {
				terms = field(tt.terms, tt.terms, 1, 1, ConfHigh)
			}
			got, ok := dueFromTerms(tt.invoice, terms)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.want, got.Value)
				assert.Equal(t, SourceDefault, got.Source)
				assert.Equal(t, ConfCheck, got.Confidence)
			}
		})
	}
}

// Lesson 10: the same input failures as sales orders, for both new types.
func TestExtract_VendorSideInputFailures(t *testing.T) {
	many := make([]prow, 0, 8)
	many = append(many, at(600, c(50, "Item"), c(110, "Description"), pdftestRight(350, "Qty"), pdftestRight(460, "Unit Price"), pdftestRight(545, "Amount")))
	for i := 0; i < 6; i++ {
		many = append(many, at(584-float64(i)*16, c(50, "A-"+string(rune('1'+i))), c(110, "Slab"), pdftestRight(350, "1"), pdftestRight(460, "$1.00"), pdftestRight(545, "$1.00")))
	}
	for _, dt := range []DocType{DocTypePurchaseOrder, DocTypeVendorBill} {
		t.Run(string(dt)+" empty", func(t *testing.T) {
			_, _, err := Extract(context.Background(), nil, dt, Options{})
			require.Error(t, err)
			assert.Equal(t, FailEmpty, inputCode(t, err))
		})
		t.Run(string(dt)+" scanned", func(t *testing.T) {
			_, _, err := Extract(context.Background(), pdftest.Build([]pdftest.Page{{}}, pdftest.Options{}), dt, Options{})
			require.Error(t, err)
			assert.Equal(t, FailScanned, inputCode(t, err))
		})
		t.Run(string(dt)+" line cap", func(t *testing.T) {
			_, _, err := Extract(context.Background(), docPDF(many), dt, Options{Limits: Limits{MaxLines: 3}})
			require.Error(t, err)
			assert.Equal(t, FailLineCap, inputCode(t, err))
		})
		t.Run(string(dt)+" no table still returns empty slices", func(t *testing.T) {
			res, _, err := Extract(context.Background(), docPDF(rows(at(700, c(50, "Notes only")))), dt, Options{})
			require.NoError(t, err)
			assert.NotNil(t, res.Lines)
			assert.NotNil(t, res.Pages)
			assert.Contains(t, res.Warnings, WarnNoLineTable)
		})
	}
}

// Header fields not found are explicit not-found fields, never zero values.
func TestParseHeader_VendorSideBlankFields(t *testing.T) {
	res := extractDoc(t, docPDF(rows(at(700, c(50, "Notes only")))), DocTypeVendorBill, nil)
	assert.Equal(t, ConfNotFound, res.Header.VendorName.Confidence)
	assert.Equal(t, ConfNotFound, res.Header.InvoiceNumber.Confidence)
	assert.Equal(t, ConfNotFound, res.Header.DueDate.Confidence)
	assert.Equal(t, ConfNotFound, res.Header.CustomerName.Confidence)
}

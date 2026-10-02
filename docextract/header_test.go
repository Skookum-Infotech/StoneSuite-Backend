package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func hdrFrom(rows ...Row) headerOut {
	return parseHeader(pagesOf(rows...), nil, HeaderOptions{})
}

func TestParseHeader_PONumber(t *testing.T) {
	tests := []struct {
		name string
		row  string
		want string
	}{
		{"PO #", "PO # 4471", "4471"},
		{"P.O. No.", "P.O. No. 88-1203", "88-1203"},
		{"Purchase Order Number", "Purchase Order Number: PO-9001", "PO-9001"},
		{"Order #", "Order # A77", "A77"},
		{"customer PO", "Customer PO: 5521", "5521"},
		{"PO box is not a PO", "PO Box 123", ""},
		{"purchase order title only", "PURCHASE ORDER", ""},
		{"echoed label", "PO No. PO-7782", "PO-7782"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := hdrFrom(trowSplit(700, tcell{50, tt.row}))
			assert.Equal(t, tt.want, out.Header.PONumber.Value)
		})
	}
}

func TestParseHeader_MultipleDocuments(t *testing.T) {
	out := hdrFrom(trowSplit(700, tcell{50, "PO # 4471"}), trowSplit(680, tcell{50, "PO # 4472"}))
	assert.Contains(t, out.Warnings, WarnMultipleDocuments)
	one := hdrFrom(trowSplit(700, tcell{50, "PO # 4471"}), trowSplit(680, tcell{50, "Customer PO: 4471"}))
	assert.NotContains(t, one.Warnings, WarnMultipleDocuments)
	sup := hdrFrom(trowSplit(700, tcell{50, "PO # 4471"}), trowSplit(680, tcell{50, "Supersedes PO 4400"}))
	assert.NotContains(t, sup.Warnings, WarnMultipleDocuments)
}

func TestParseHeader_Dates(t *testing.T) {
	tests := []struct {
		name         string
		row          string
		wantOrder    string
		wantDelivery string
		wantWarn     bool
	}{
		{"order date", "Order Date: 01/02/2026", "2026-01-02", "", false},
		{"bare date", "Date: 1/2/26", "2026-01-02", "", false},
		{"word date", "Date: Jan 2, 2026", "2026-01-02", "", false},
		{"delivery", "Required By: 03/01/2026", "", "2026-03-01", false},
		{"ship date", "Ship Date 03/01/2026", "", "2026-03-01", false},
		{"both on one row", "Order Date: 01/02/2026 Delivery Date: 02/01/2026", "2026-01-02", "2026-02-01", false},
		{"impossible date is flagged not guessed", "Order Date: 02/30/2026", "02/30/2026", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := hdrFrom(trowSplit(700, tcell{50, tt.row}))
			assert.Equal(t, tt.wantOrder, out.Header.OrderDate.Value)
			assert.Equal(t, tt.wantDelivery, out.Header.DeliveryDate.Value)
			assert.Equal(t, tt.wantWarn, contains(out.Warnings, WarnInvalidDate))
			if tt.wantWarn {
				assert.Equal(t, ConfCheck, out.Header.OrderDate.Confidence)
			}
		})
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestParseHeader_Parties(t *testing.T) {
	side := []Row{
		trow(700, tcell{50, "Bill To:"}, tcell{320, "Ship To:"}),
		trow(686, tcell{50, "ACME Stone Inc"}, tcell{320, "Plano Yard"}),
		trow(672, tcell{50, "12 Main Street"}, tcell{320, "900 Quarry Road"}),
		trowSplit(630, tcell{50, "Order Date: 01/02/2026"}),
	}
	out := hdrFrom(side...)
	assert.Equal(t, "ACME Stone Inc", out.Header.CustomerName.Value)
	assert.Equal(t, "ACME Stone Inc\n12 Main Street", out.Header.BillTo.Value)
	assert.Equal(t, "Plano Yard\n900 Quarry Road", out.Header.ShipTo.Value)

	inline := hdrFrom(trowSplit(700, tcell{50, "Customer: Bedrock Builders LLC"}), trow(686, tcell{50, "88 Industrial Way"}))
	assert.Equal(t, "Bedrock Builders LLC", inline.Header.CustomerName.Value)

	own := parseHeader(pagesOf(
		trowSplit(700, tcell{50, "Bill To:"}), trowSplit(686, tcell{50, "Elevation Stone, LLC"}),
		trowSplit(640, tcell{50, "Sold To:"}), trowSplit(626, tcell{50, "Harbor Design Group"}),
	), nil, HeaderOptions{OwnCompanyNames: []string{"Elevation Stone"}})
	assert.Equal(t, "Harbor Design Group", own.Header.CustomerName.Value)

	none := hdrFrom(trowSplit(700, tcell{50, "Hello there"}))
	assert.False(t, none.Header.CustomerName.Found())
	assert.Equal(t, ConfNotFound, none.Header.CustomerName.Confidence)
}

func TestParseHeader_LabelStrip(t *testing.T) {
	out := hdrFrom(
		trow(700, tcell{50, "PO NUMBER"}, tcell{200, "ORDER DATE"}, tcell{350, "TERMS"}),
		trow(686, tcell{50, "A-77391"}, tcell{200, "09/09/2026"}, tcell{350, "Net 45"}),
	)
	assert.Equal(t, "A-77391", out.Header.PONumber.Value)
	assert.Equal(t, "2026-09-09", out.Header.OrderDate.Value)
	assert.Equal(t, "Net 45", out.Header.PaymentTerms.Value)
}

func TestParseHeader_Totals(t *testing.T) {
	tests := []struct {
		name string
		rows []string
		get  func(Header) Field
		want string
		conf Confidence
	}{
		{"subtotal", []string{"Subtotal $1,234.56"}, func(h Header) Field { return h.Subtotal }, "1234.56", ConfHigh},
		{"sales tax with percent", []string{"Sales Tax (8.25%): $82.50"}, func(h Header) Field { return h.Tax }, "82.50", ConfHigh},
		{"freight", []string{"Freight $150.00"}, func(h Header) Field { return h.Shipping }, "150.00", ConfHigh},
		{"discount", []string{"Discount ($25.00)"}, func(h Header) Field { return h.Discount }, "-25.00", ConfHigh},
		{"grand total beats total", []string{"Total $10.00", "Grand Total $99.00"}, func(h Header) Field { return h.Total }, "99.00", ConfHigh},
		{"amount due", []string{"Amount Due: $500.00"}, func(h Header) Field { return h.Total }, "500.00", ConfHigh},
		{"total qty is not a total", []string{"Total Qty 12"}, func(h Header) Field { return h.Total }, "", ConfNotFound},
		{"tax id is not tax", []string{"Tax ID: 12-3456789"}, func(h Header) Field { return h.Tax }, "", ConfNotFound},
		{"EU decimal flagged", []string{"Total 1.234,56"}, func(h Header) Field { return h.Total }, "1.234,56", ConfCheck},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rows []Row
			for i, s := range tt.rows {
				rows = append(rows, trowSplit(float64(700-i*14), tcell{50, s}))
			}
			f := tt.get(hdrFrom(rows...).Header)
			assert.Equal(t, tt.want, f.Value)
			assert.Equal(t, tt.conf, f.Confidence)
		})
	}
}

func TestParseHeader_TermsAndCurrency(t *testing.T) {
	tests := []struct {
		name         string
		row          string
		wantTerms    string
		wantCurrency string
		wantWarn     []string
	}{
		{"labelled terms", "Terms: Net 30", "Net 30", "USD", nil},
		{"unlabelled net terms", "Payment due Net 45 days", "Net 45", "USD", nil},
		{"terms and conditions heading", "Terms and Conditions apply", "", "USD", nil},
		{"euro symbol", "Total €100.00", "", "EUR", []string{WarnNonUSDCurrency}},
		{"gbp code", "Prices in GBP", "", "GBP", []string{WarnNonUSDCurrency}},
		{"explicit usd", "All amounts in USD", "", "USD", nil},
		{"tax inclusive", "Prices are tax inclusive", "", "USD", []string{WarnTaxInclusive}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := hdrFrom(trowSplit(700, tcell{50, tt.row}))
			assert.Equal(t, tt.wantTerms, out.Header.PaymentTerms.Value)
			assert.Equal(t, tt.wantCurrency, out.Header.Currency.Value)
			for _, w := range tt.wantWarn {
				assert.Contains(t, out.Warnings, w)
			}
			if tt.wantWarn == nil {
				assert.NotContains(t, out.Warnings, WarnNonUSDCurrency)
			}
		})
	}
}

func TestParseHeader_ProvenanceSnippet(t *testing.T) {
	long := "PO Number: 4471 " + string(make([]byte, 0))
	for i := 0; i < 40; i++ {
		long += "padding "
	}
	out := hdrFrom(trowSplit(700, tcell{50, long}))
	f := out.Header.PONumber
	assert.Equal(t, "4471", f.Value)
	assert.LessOrEqual(t, len(f.Snippet), MaxSnippetLen)
	assert.Equal(t, 1, f.Page)
	assert.Equal(t, 1, f.Row)
	assert.Equal(t, SourceDocument, f.Source)
}

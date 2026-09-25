package docpdf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCP1252Str(t *testing.T) {
	c := cp1252{translate: newDoc().UnicodeTranslatorFromDescriptor("")}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ascii is untouched", "Plain text 123", "Plain text 123"},
		{"accented latin", "Café Müller", "Caf\xe9 M\xfcller"},
		{"em dash", "a — b", "a \x97 b"},
		{"curly quotes", "“hi”", "\x93hi\x94"},
		{"euro sign", "€5", "\x805"},
		{"rupee sign is spelled out", "₹500", "Rs.500"},
		{"unrepresentable characters become question marks", "日本", "??"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, c.str(tc.in))
		})
	}
}

func TestToCP1252_DoesNotMutateInput(t *testing.T) {
	d := sampleDoc()
	d.Lines[0].Name = "Slab — polished"
	got := toCP1252(newDoc(), d)
	assert.Equal(t, "Slab — polished", d.Lines[0].Name, "the caller's document must be left alone")
	assert.Equal(t, "Slab \x97 polished", got.Lines[0].Name)
}

func TestMoney(t *testing.T) {
	tests := []struct {
		name string
		sym  string
		v    float64
		want string
	}{
		{"positive", "$", 5, "$5.00"},
		{"default symbol", "", 5, "$5.00"},
		{"minus goes before the symbol", "€", -5.5, "-€5.50"},
		{"tiny negative is not negative zero", "$", -0.001, "$0.00"},
		{"zero", "$", 0, "$0.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, money(tc.sym, tc.v))
		})
	}
}

func TestTrimNum(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{{3, "3"}, {3.5, "3.5"}, {8.25, "8.25"}, {0, "0"}, {100, "100"}}
	for _, tc := range tests {
		assert.Equal(t, tc.want, trimNum(tc.in))
	}
}

func TestPctCell(t *testing.T) {
	assert.Equal(t, enDash, pctCell(0), "zero reads as none")
	assert.Equal(t, "8.25", pctCell(8.25))
}

func TestLineName(t *testing.T) {
	assert.Equal(t, "Slab", lineName(PrintLine{Name: "Slab"}))
	assert.Equal(t, "S1 "+emDash+" Slab", lineName(PrintLine{SKU: "S1", Name: "Slab"}))
}

func TestAddressEmpty(t *testing.T) {
	assert.True(t, Address{}.empty())
	assert.False(t, Address{Email: "a@b.example"}.empty())
}

func TestAmountHeadline(t *testing.T) {
	tests := []struct {
		name      string
		doc       PrintableDoc
		wantLabel string
		wantValue float64
		wantOK    bool
	}{
		{"payment-tracked document shows the balance due", PrintableDoc{ShowBalance: true, BalanceDue: 60, GrandTotal: 100}, "AMOUNT DUE", 60, true},
		{"a fully paid document still shows its zero balance", PrintableDoc{ShowBalance: true, GrandTotal: 100}, "AMOUNT DUE", 0, true},
		{"untracked document shows its total", PrintableDoc{Lines: []PrintLine{{Name: "x"}}, GrandTotal: 100}, "TOTAL", 100, true},
		{"document with no lines and no total has nothing to headline", PrintableDoc{}, "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			label, value, ok := amountHeadline(tc.doc)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantLabel, label)
			assert.InDelta(t, tc.wantValue, value, 0.001)
		})
	}
}

func TestMetaRows(t *testing.T) {
	withAmount := PrintableDoc{ShowBalance: true, BalanceDue: 10}
	tests := []struct {
		name string
		doc  PrintableDoc
		want []string
	}{
		{"dates only; status yields to the amount box", PrintableDoc{IssueDate: "d1", DueDate: "d2", Status: "Sent", ShowBalance: true, BalanceDue: 10}, []string{"ISSUE DATE", "DUE DATE"}},
		{"empty dates are omitted", withAmount, nil},
		{"no amount: status stays so the document still shows its state", PrintableDoc{IssueDate: "d1", Status: "Active"}, []string{"ISSUE DATE", "STATUS"}},
		{"nothing to show", PrintableDoc{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range metaRows(tc.doc) {
				got = append(got, r.label)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTotalsRows(t *testing.T) {
	labels := func(rows []totalsRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.label)
		}
		return out
	}
	tests := []struct {
		name         string
		doc          PrintableDoc
		wantLabels   []string
		wantHeadline string
	}{
		{
			"subtotal only",
			PrintableDoc{Subtotal: 100, GrandTotal: 100},
			[]string{"Subtotal"}, "Grand Total",
		},
		{
			"adjustments appear only when non-zero",
			PrintableDoc{Subtotal: 100, DiscountTotal: 10, TaxTotal: 5, ShippingCharge: 2, Adjustment: -1, GrandTotal: 96},
			[]string{"Subtotal", "Discount", "Tax", "Shipping", "Adjustment"}, "Grand Total",
		},
		{
			"payment tracking headlines the balance",
			PrintableDoc{Subtotal: 100, GrandTotal: 100, AmountPaid: 40, BalanceDue: 60, ShowBalance: true},
			[]string{"Subtotal", "Grand Total", "Amount Paid"}, "Balance Due",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, headline := totalsRows(tc.doc)
			assert.Equal(t, tc.wantLabels, labels(rows))
			assert.Equal(t, tc.wantHeadline, headline.label)
		})
	}

	rows, _ := totalsRows(PrintableDoc{DiscountTotal: 10, AmountPaid: 40, ShowBalance: true})
	for _, r := range rows {
		if r.label == "Discount" || r.label == "Amount Paid" {
			assert.Negative(t, r.value, "%s is shown as a deduction", r.label)
		}
	}
}

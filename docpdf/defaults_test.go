package docpdf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithDefaultWording(t *testing.T) {
	defaults := map[string]Wording{
		"INVOICE":     {Terms: "Net 30", Notes: "Thanks"},
		"SALES ORDER": {Terms: "Deposit first", Notes: "Call us"},
	}
	tests := []struct {
		name      string
		in        PrintableDoc
		wantTerms string
		wantNotes string
	}{
		{"blank invoice gets tenant defaults", PrintableDoc{Kind: "INVOICE", Seller: Seller{Defaults: defaults}}, "Net 30", "Thanks"},
		{"kind match ignores case and spaces", PrintableDoc{Kind: " sales order ", Seller: Seller{Defaults: defaults}}, "Deposit first", "Call us"},
		{"record text wins", PrintableDoc{Kind: "INVOICE", Terms: "Net 15", Notes: "Custom", Seller: Seller{Defaults: defaults}}, "Net 15", "Custom"},
		{"only the blank field is filled", PrintableDoc{Kind: "INVOICE", Terms: "Custom terms", Seller: Seller{Defaults: defaults}}, "Custom terms", "Thanks"},
		{"whitespace-only counts as blank", PrintableDoc{Kind: "INVOICE", Terms: "  ", Notes: "\n", Seller: Seller{Defaults: defaults}}, "Net 30", "Thanks"},
		{"kind without a default stays blank", PrintableDoc{Kind: "QUOTE", Seller: Seller{Defaults: defaults}}, "", ""},
		{"tenant with no defaults stays blank", PrintableDoc{Kind: "INVOICE"}, "", ""},
		{"purchase kinds get none", PrintableDoc{Kind: "PURCHASE ORDER", Seller: Seller{Defaults: defaults}}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withDefaultWording(tc.in)
			assert.Equal(t, tc.wantTerms, got.Terms)
			assert.Equal(t, tc.wantNotes, got.Notes)
		})
	}
}

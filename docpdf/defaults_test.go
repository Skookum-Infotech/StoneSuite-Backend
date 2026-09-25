package docpdf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithDefaultWording(t *testing.T) {
	tests := []struct {
		name      string
		in        PrintableDoc
		wantTerms string
		wantNotes string
	}{
		{"blank estimate gets defaults", PrintableDoc{Kind: "ESTIMATE"}, defaultWordingByKind["ESTIMATE"].terms, defaultWordingByKind["ESTIMATE"].notes},
		{"kind match ignores case and spaces", PrintableDoc{Kind: " invoice "}, defaultWordingByKind["INVOICE"].terms, defaultWordingByKind["INVOICE"].notes},
		{"record text wins", PrintableDoc{Kind: "QUOTE", Terms: "Net 15", Notes: "Custom"}, "Net 15", "Custom"},
		{"only the blank field is filled", PrintableDoc{Kind: "SALES ORDER", Terms: "Custom terms"}, "Custom terms", defaultWordingByKind["SALES ORDER"].notes},
		{"whitespace-only counts as blank", PrintableDoc{Kind: "ESTIMATE", Terms: "  ", Notes: "\n"}, defaultWordingByKind["ESTIMATE"].terms, defaultWordingByKind["ESTIMATE"].notes},
		{"purchase kinds get none", PrintableDoc{Kind: "PURCHASE ORDER"}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withDefaultWording(tc.in)
			assert.Equal(t, tc.wantTerms, got.Terms)
			assert.Equal(t, tc.wantNotes, got.Notes)
		})
	}
}

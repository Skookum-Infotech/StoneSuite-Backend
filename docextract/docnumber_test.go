package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeDocNumber(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "4471", "4471"},
		{"glued po label", "po7", "7"},
		{"glued inv label with zeros", "INV0042", "42"},
		{"word starting with label kept", "porter-12", "porter12"},
		{"hash prefix", "#4471", "4471"},
		{"inv prefix", "INV-4471", "4471"},
		{"leading zeros", "000123", "123"},
		{"inv and zeros", "INV-000123", "123"},
		{"case folded", "ab-77", "ab77"},
		{"po label", "PO 4471", "4471"},
		{"po dash", "po-4471", "4471"},
		{"no dot", "No. 88", "88"},
		{"separators dropped", "A-12/B", "a12b"},
		{"all zeros", "000", "0"},
		{"empty", "", ""},
		{"separators only", " - # ", ""},
		{"letters first kept", "SORD-000123", "sord000123"},
		{"surrounding space", "  INV-9 ", "9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeDocNumber(tc.in))
		})
	}
}

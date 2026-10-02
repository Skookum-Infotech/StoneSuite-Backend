package docextractjob

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeSKU(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"case", "AB-123", "ab123"},
		{"spaces dashes underscores dots", " ab 1_2.3-4 ", "ab1234"},
		{"variant suffix kept", "SLAB-3CM", "slab3cm"},
		{"variant differs from base", "SLAB", "slab"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, NormalizeSKU(tc.in)) })
	}
	assert.NotEqual(t, NormalizeSKU("SLAB"), NormalizeSKU("SLAB-3CM"))
}

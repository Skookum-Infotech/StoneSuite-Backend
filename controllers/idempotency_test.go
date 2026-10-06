package controllers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseIdempotencyKey(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantKey string
		wantOK  bool
	}{
		{"absent", "", "", true},
		{"whitespace only", "   ", "", true},
		{"trimmed", "  abc-123 ", "abc-123", true},
		{"at limit", strings.Repeat("a", maxIdempotencyKeyLen), strings.Repeat("a", maxIdempotencyKeyLen), true},
		{"over limit", strings.Repeat("a", maxIdempotencyKeyLen+1), "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := parseIdempotencyKey(tc.raw)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantKey, key)
			}
		})
	}
}

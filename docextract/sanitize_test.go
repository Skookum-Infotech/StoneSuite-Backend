package docextract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"collapse whitespace", "  a \t b\n\n c  ", 0, "a b c"},
		{"control dropped", "a\x00b\x07c", 0, "abc"},
		{"bidi dropped", "a\u202eb\u2066c\u200bd", 0, "abcd"},
		{"clip runes", "héllo wörld", 5, "héllo"},
		{"clip trims", "ab cd", 3, "ab"},
		{"empty", "  ", 10, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, Clean(tc.in, tc.max)) })
	}
}

func TestCleanBlock(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"keeps newlines", "a  b\r\nc\n\n\nd", 0, "a b\nc\nd"},
		{"strips bidi", "a\u202e\nb", 0, "a\nb"},
		{"clip", "abc\ndef", 5, "abc\nd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, CleanBlock(tc.in, tc.max)) })
	}
}

func TestMaskSensitive(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"luhn card plain", "Card 4111111111111111 on file", "Card [masked] on file"},
		{"luhn card spaced", "4111 1111 1111 1111", "[masked]"},
		{"luhn card dashed", "pay 4111-1111-1111-1111.", "pay [masked]."},
		{"iban", "IBAN GB82WEST12345698765432 end", "IBAN [masked] end"},
		{"iban spaced", "IBAN GB82 WEST 1234 5698 7654 32", "IBAN [masked]"},
		{"routing", "Routing: 021000021", "Routing: [masked]"},
		{"aba no", "ABA No. 021000021", "ABA No. [masked]"},
		{"account hash", "Acct # 123456789012", "Acct # [masked]"},
		{"account no lower", "account no: 000123456789", "account no: [masked]"},
		{"ach", "ACH 021 000 021", "ACH [masked]"},
		{"po number", "PO 20481937", "PO 20481937"},
		{"sku non-luhn", "SKU 1234567890123", "SKU 1234567890123"},
		{"price", "$12,345.67", "$12,345.67"},
		{"qty", "Qty 1,250", "Qty 1,250"},
		{"date", "2026-10-22", "2026-10-22"},
		{"phone", "Call (555) 123-4567", "Call (555) 123-4567"},
		{"short account number", "Account 12345", "Account 12345"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, MaskSensitive(tc.in)) })
	}
}

func TestBuildPromptMasksResidual(t *testing.T) {
	residual := "Order 20481937\nCard 4111 1111 1111 1111\nRouting: 021000021\nTotal $12,345.67"
	_, user, err := BuildPromptFor(DocTypeSalesOrder, residual, []string{KeyPONumber}, 0)
	assert.NoError(t, err)
	assert.Contains(t, user, MaskedPlaceholder)
	assert.NotContains(t, user, "4111")
	assert.False(t, strings.Contains(user, "021000021"))
	assert.Contains(t, user, "20481937")
	assert.Contains(t, user, "$12,345.67")
}

package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBoolStr covers the audit-trail bool rendering used by every toggle
// branch in Update.
func TestBoolStr(t *testing.T) {
	tests := []struct {
		name string
		in   bool
		want string
	}{
		{name: "true renders true", in: true, want: "true"},
		{name: "false renders false", in: false, want: "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, boolStr(tt.in))
		})
	}
}

// TestMissingRequiredAttrs exercises the re-validation-on-type-change guard
// (review finding 2) including the encrypted-attributes shape that a full
// ValidateAttributes call cannot safely be run against: stored bank
// attributes carry a ciphertext accountNumber and a derived
// accountNumberLast4 key that is not in any type's allowed key set.
func TestMissingRequiredAttrs(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		attrs       map[string]any
		want        []string
	}{
		{
			name:        "type with no required fields and empty attrs",
			accountType: "general",
			attrs:       map[string]any{},
			want:        nil,
		},
		{
			name:        "bank type with no attrs at all is missing both required keys",
			accountType: "bank",
			attrs:       map[string]any{},
			want:        []string{"accountNumber", "bankName"},
		},
		{
			name:        "bank type missing only accountNumber",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "HDFC"},
			want:        []string{"accountNumber"},
		},
		{
			name:        "bank type with blank required value counts as missing",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "  ", "accountNumber": "1234567890124821"},
			want:        []string{"bankName"},
		},
		{
			name:        "bank type with non-string required value counts as missing",
			accountType: "bank",
			attrs:       map[string]any{"bankName": 12345, "accountNumber": "1234567890124821"},
			want:        []string{"bankName"},
		},
		{
			name:        "bank type satisfied by the ENCRYPTED stored shape",
			accountType: "bank",
			attrs: map[string]any{
				"bankName":           "HDFC",
				BankAccountNumberKey: "ciphertext-not-plaintext==",
				"accountNumberLast4": "4821", // derived key EncryptAttributes writes; not in schema
			},
			want: nil,
		},
		{
			name:        "credit_card type missing issuer and last4",
			accountType: "credit_card",
			attrs:       map[string]any{"network": "visa"},
			want:        []string{"issuer", "last4"},
		},
		{
			name:        "credit_card type fully satisfied",
			accountType: "credit_card",
			attrs:       map[string]any{"issuer": "Chase", "last4": "1234"},
			want:        nil,
		},
		{
			name:        "unknown account type has no schema, so nothing is reported missing",
			accountType: "not-a-real-type",
			attrs:       map[string]any{},
			want:        nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := missingRequiredAttrs(tt.accountType, tt.attrs)
			assert.Equal(t, tt.want, got)
		})
	}
}

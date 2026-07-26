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

// TestDisallowedAttrs exercises the symmetric counterpart to
// missingRequiredAttrs (review finding 5): a type change must reject keys the
// new type forbids, not just keys it requires, without flagging the
// server-derived accountNumberLast4 hint that is never in any type's schema.
func TestDisallowedAttrs(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		attrs       map[string]any
		want        []string
	}{
		{
			name:        "general with empty attrs has nothing disallowed",
			accountType: "general",
			attrs:       map[string]any{},
			want:        nil,
		},
		{
			name:        "bank account's own keys are all allowed under bank",
			accountType: "bank",
			attrs: map[string]any{
				"bankName":           "HDFC",
				BankAccountNumberKey: "ciphertext==",
				"accountNumberLast4": "4821",
			},
			want: nil,
		},
		{
			name:        "changing bank to general disallows the bank-only keys",
			accountType: "general",
			attrs: map[string]any{
				"bankName":           "HDFC",
				BankAccountNumberKey: "ciphertext==",
				"accountNumberLast4": "4821", // server-derived, always skipped
			},
			want: []string{"accountNumber", "bankName"},
		},
		{
			name:        "changing bank to tax keeps neither bank key",
			accountType: "tax",
			attrs: map[string]any{
				"bankName":           "HDFC",
				BankAccountNumberKey: "ciphertext==",
			},
			want: []string{"accountNumber", "bankName"},
		},
		{
			name:        "unknown account type disallows every key",
			accountType: "not-a-real-type",
			attrs:       map[string]any{"bankName": "HDFC"},
			want:        []string{"bankName"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := disallowedAttrs(tt.accountType, tt.attrs)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestChangedString covers the trim-and-compare helper shared by the Name and
// Description branches (review finding 9), including the whitespace-only
// phantom-audit-row regression it exists to prevent.
func TestChangedString(t *testing.T) {
	tests := []struct {
		name   string
		in     *string
		cur    string
		want   string
		wantOK bool
	}{
		{
			name:   "nil input means no change",
			in:     nil,
			cur:    "Foo",
			want:   "",
			wantOK: false,
		},
		{
			name:   "whitespace-padded value equal after trim is no change",
			in:     ptrString(" Foo "),
			cur:    "Foo",
			want:   "",
			wantOK: false,
		},
		{
			name:   "empty string differs from a non-empty current value",
			in:     ptrString(""),
			cur:    "Foo",
			want:   "",
			wantOK: true,
		},
		{
			name:   "whitespace-only value equal to empty current is no change",
			in:     ptrString("  "),
			cur:    "",
			want:   "",
			wantOK: false,
		},
		{
			name:   "a real change is reported with the trimmed value",
			in:     ptrString("  Bar  "),
			cur:    "Foo",
			want:   "Bar",
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := changedString(tt.in, tt.cur)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ptrString returns a pointer to s, for building *string test fixtures inline.
func ptrString(s string) *string { return &s }

package chartofaccounts

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/secret"
)

func testCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	c, err := secret.New(key)
	require.NoError(t, err)
	return c
}

func TestLast4(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"long number", "1234567890124821", "4821"},
		{"exactly four", "4821", "4821"},
		{"three digits", "821", "821"},
		{"one digit", "8", "8"},
		{"empty", "", ""},
		{"trailing spaces trimmed", "1234567890124821  ", "4821"},
		{"multi-byte runes", "日本語テスト", "語テスト"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Last4(tt.in)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "Last4 must not slice mid-rune")
		})
	}
}

func TestEncryptAttributesRoundTrip(t *testing.T) {
	c := testCipher(t)
	in := map[string]any{"bankName": "HDFC", BankAccountNumberKey: "1234567890124821"}

	enc, err := EncryptAttributes(c, in)
	require.NoError(t, err)

	// Non-sensitive keys pass through untouched.
	assert.Equal(t, "HDFC", enc["bankName"])
	// The plaintext number is gone, replaced by ciphertext plus a last-4 hint.
	assert.NotEqual(t, "1234567890124821", enc[BankAccountNumberKey])
	assert.Equal(t, "4821", enc["accountNumberLast4"])

	back, err := c.Decrypt(enc[BankAccountNumberKey].(string))
	require.NoError(t, err)
	assert.Equal(t, "1234567890124821", back)
}

func TestEncryptAttributesFailsClosedWithoutCipher(t *testing.T) {
	_, err := EncryptAttributes(nil, map[string]any{BankAccountNumberKey: "1234567890124821"})
	require.ErrorIs(t, err, ErrCipherUnavailable)
}

func TestEncryptAttributesNoCipherNeededWithoutBankNumber(t *testing.T) {
	got, err := EncryptAttributes(nil, map[string]any{"bankName": "HDFC"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"bankName": "HDFC"}, got)
}

func TestEncryptAttributesRejectsNonStringAccountNumber(t *testing.T) {
	tests := []struct {
		name string
		val  any
	}{
		{"int", 1234567890124821},
		{"float64", 1234567890.0},
		{"nested map", map[string]any{"iban": "GB33BUKB20201555555555"}},
		{"nil", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testCipher(t)
			got, err := EncryptAttributes(c, map[string]any{
				"bankName":           "HDFC",
				BankAccountNumberKey: tt.val,
			})
			require.Error(t, err)
			assert.True(t, IsClientError(err), "non-string account number must be a ClientError")
			assert.Nil(t, got)
			assert.NotContains(t, err.Error(), "1234567890124821",
				"error message must never carry the raw value")
		})
	}
}

func TestEncryptAttributesDropsBlankAccountNumber(t *testing.T) {
	tests := []struct{ name, val string }{
		{"empty string", ""},
		{"whitespace only", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testCipher(t)
			got, err := EncryptAttributes(c, map[string]any{
				"bankName":           "HDFC",
				BankAccountNumberKey: tt.val,
			})
			require.NoError(t, err)
			assert.NotContains(t, got, BankAccountNumberKey,
				"a blank account number must be dropped, not stored empty")
			assert.NotContains(t, got, "accountNumberLast4",
				"no last4 hint should be written when nothing was encrypted")
			assert.Equal(t, "HDFC", got["bankName"])
		})
	}
}

func TestEncryptAttributesStripsCallerSuppliedLast4WhenNoNumberEncrypted(t *testing.T) {
	got, err := EncryptAttributes(nil, map[string]any{
		"bankName":           "HDFC",
		"accountNumberLast4": "9999",
	})
	require.NoError(t, err)
	assert.NotContains(t, got, "accountNumberLast4",
		"a caller-supplied hint must never survive without a real encrypted number")
}

func TestEncryptAttributesOverwritesCallerSuppliedLast4(t *testing.T) {
	c := testCipher(t)
	got, err := EncryptAttributes(c, map[string]any{
		"bankName":           "HDFC",
		BankAccountNumberKey: "1234567890124821",
		"accountNumberLast4": "9999",
	})
	require.NoError(t, err)
	assert.Equal(t, "4821", got["accountNumberLast4"],
		"the server-derived last4 must overwrite any caller-supplied hint")
}

func TestMaskAttributesRemovesCiphertext(t *testing.T) {
	masked := MaskAttributes(map[string]any{
		"bankName":           "HDFC",
		BankAccountNumberKey: "ZW5jcnlwdGVkLWJsb2I=",
		"accountNumberLast4": "4821",
	})
	assert.Equal(t, "HDFC", masked["bankName"])
	assert.Equal(t, "4821", masked["accountNumberLast4"])
	assert.NotContains(t, masked, BankAccountNumberKey,
		"ciphertext must never leave the store layer")
}

func TestMaskedAttributesNeverSerialiseTheNumber(t *testing.T) {
	c := testCipher(t)
	enc, err := EncryptAttributes(c, map[string]any{
		"bankName": "HDFC", BankAccountNumberKey: "1234567890124821",
	})
	require.NoError(t, err)

	blob, err := json.Marshal(Account{Attributes: MaskAttributes(enc)})
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "1234567890124821")
	assert.NotContains(t, string(blob), enc[BankAccountNumberKey].(string))
	assert.Contains(t, string(blob), "4821")
}

func TestMaskAttributesNil(t *testing.T) {
	assert.Equal(t, map[string]any{}, MaskAttributes(nil))
}

func TestEncryptAndMaskAttributesDoNotMutateInput(t *testing.T) {
	c := testCipher(t)
	in := map[string]any{"bankName": "HDFC", BankAccountNumberKey: "1234567890124821"}

	_, err := EncryptAttributes(c, in)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"bankName": "HDFC", BankAccountNumberKey: "1234567890124821"}, in,
		"EncryptAttributes must not mutate its input map")

	encForMask := map[string]any{
		"bankName":           "HDFC",
		BankAccountNumberKey: "ZW5jcnlwdGVkLWJsb2I=",
		"accountNumberLast4": "4821",
	}
	_ = MaskAttributes(encForMask)
	assert.Equal(t, map[string]any{
		"bankName":           "HDFC",
		BankAccountNumberKey: "ZW5jcnlwdGVkLWJsb2I=",
		"accountNumberLast4": "4821",
	}, encForMask, "MaskAttributes must not mutate its input map")
}

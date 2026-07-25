package chartofaccounts

import (
	"encoding/base64"
	"encoding/json"
	"testing"

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Last4(tt.in))
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

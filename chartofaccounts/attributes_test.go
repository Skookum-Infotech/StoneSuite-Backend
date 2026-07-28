package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAttributes(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		attrs       map[string]any
		wantErr     string // substring; empty means success
		wantKeys    []string
	}{
		{
			name:        "general accepts empty attributes",
			accountType: "general",
			attrs:       map[string]any{},
			wantKeys:    []string{},
		},
		{
			name:        "general rejects any key",
			accountType: "general",
			attrs:       map[string]any{"bankName": "HDFC"},
			wantErr:     "bankName",
		},
		{
			name:        "bank accepts the full set",
			accountType: "bank",
			attrs: map[string]any{
				"bankName": "HDFC", "branch": "NYC",
				"accountNumber": "1234567890124821",
				"routingNumber": "021000021", "swift": "HDFCUS33",
			},
			wantKeys: []string{"accountNumber", "bankName", "branch", "routingNumber", "swift"},
		},
		{
			name:        "bank requires bankName",
			accountType: "bank",
			attrs:       map[string]any{"accountNumber": "123456"},
			wantErr:     "bankName",
		},
		{
			name:        "bank requires accountNumber",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "HDFC"},
			wantErr:     "accountNumber",
		},
		{
			name:        "bank rejects an unknown key",
			accountType: "bank",
			attrs: map[string]any{
				"bankName": "HDFC", "accountNumber": "123456", "iban": "GB33BUKB",
			},
			wantErr: "iban",
		},
		{
			name:        "bank rejects a non-string value",
			accountType: "bank",
			attrs:       map[string]any{"bankName": 42, "accountNumber": "123456"},
			wantErr:     "bankName",
		},
		{
			name:        "credit_card requires issuer and last4",
			accountType: "credit_card",
			attrs:       map[string]any{"issuer": "Amex", "last4": "1005"},
			wantKeys:    []string{"issuer", "last4"},
		},
		{
			name:        "credit_card rejects a missing last4",
			accountType: "credit_card",
			attrs:       map[string]any{"issuer": "Amex"},
			wantErr:     "last4",
		},
		{
			name:        "cash accepts an optional location",
			accountType: "cash",
			attrs:       map[string]any{"location": "Front desk"},
			wantKeys:    []string{"location"},
		},
		{
			name:        "tax accepts an optional registration number",
			accountType: "tax",
			attrs:       map[string]any{"taxRegistrationNumber": "US-123"},
			wantKeys:    []string{"taxRegistrationNumber"},
		},
		{
			name:        "unknown account type is rejected",
			accountType: "crypto_wallet",
			attrs:       map[string]any{},
			wantErr:     "crypto_wallet",
		},
		{
			name:        "nil attributes normalise to empty",
			accountType: "general",
			attrs:       nil,
			wantKeys:    []string{},
		},
		{
			name:        "blank required value is rejected",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "   ", "accountNumber": "123456"},
			wantErr:     "bankName",
		},
		{
			name:        "bank reports the alphabetically first of two missing required fields",
			accountType: "bank",
			attrs:       map[string]any{},
			wantErr:     "accountNumber",
		},
		{
			name:        "cash drops a whitespace-only optional value",
			accountType: "cash",
			attrs:       map[string]any{"location": "   "},
			wantKeys:    []string{},
		},
		{
			name:        "bank trims accepted values",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "  HDFC  ", "accountNumber": "123456"},
			wantKeys:    []string{"bankName", "accountNumber"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateAttributes(tt.accountType, tt.attrs)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "want ClientError, got %T", err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, len(tt.wantKeys))
			for _, k := range tt.wantKeys {
				assert.Contains(t, got, k)
			}
		})
	}
}

// TestValidateAttributesTrimsAcceptedValues verifies the doc comment's claim
// that accepted string values are normalised (trimmed), not just accepted.
func TestValidateAttributesTrimsAcceptedValues(t *testing.T) {
	got, err := ValidateAttributes("bank", map[string]any{
		"bankName": "  HDFC  ", "accountNumber": "123456",
	})
	require.NoError(t, err)
	assert.Equal(t, "HDFC", got["bankName"])
}

// TestValidateAttributesUnknownKeyErrorIsDeterministic proves that when two
// unknown keys are present simultaneously, the error always names the
// alphabetically-first one, across many runs, despite Go's randomised map
// iteration order.
func TestValidateAttributesUnknownKeyErrorIsDeterministic(t *testing.T) {
	attrs := map[string]any{
		"bankName":      "HDFC",
		"accountNumber": "123456",
		"iban":          "GB33BUKB",
		"zzz":           "unexpected",
	}
	for i := 0; i < 20; i++ {
		_, err := ValidateAttributes("bank", attrs)
		require.Error(t, err)
		assert.True(t, IsClientError(err), "want ClientError, got %T", err)
		assert.Contains(t, err.Error(), "iban")
		assert.NotContains(t, err.Error(), "zzz")
	}
}

func TestValidAccountTypes(t *testing.T) {
	// Must match chk_coa_type in tenant/schema.sql exactly, and must be sorted.
	assert.Equal(t, []string{
		"ap", "ar", "bank", "cash", "credit_card",
		"fixed_asset", "general", "inventory", "tax",
	}, ValidAccountTypes())
}

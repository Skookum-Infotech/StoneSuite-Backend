package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValueSafeFieldsIsFailClosed pins the inverted redaction rule (AD-10):
// a history field is redacted unless it was deliberately allowlisted. The
// previous rule redacted only Field == BankAccountNumberKey, which meant any
// future caller recording sensitive data under a different field name would
// have written it to the audit trail in plaintext.
func TestValueSafeFieldsIsFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		field string
		safe  bool
	}{
		{name: "code carries values", field: "code", safe: true},
		{name: "name carries values", field: "name", safe: true},
		{name: "description carries values", field: "description", safe: true},
		{name: "type carries values", field: "type", safe: true},
		{name: "is_active carries values", field: "is_active", safe: true},
		{name: "is_postable carries values", field: "is_postable", safe: true},
		{name: "is_visible carries values", field: "is_visible", safe: true},
		{name: "slot repoint carries account codes", field: "coa_account_id", safe: true},

		{name: "attributes is redacted", field: "attributes", safe: false},
		{name: "the bank number key is redacted", field: BankAccountNumberKey, safe: false},
		{name: "the derived last4 key is redacted", field: accountNumberLast4Key, safe: false},
		{name: "an unknown future field is redacted by default", field: "someNewField", safe: false},
		{name: "the empty field is redacted", field: "", safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.safe, valueSafeFields[tt.field])
		})
	}
}

// TestEveryEmittedHistoryFieldIsAccountedFor guards the allowlist against
// drift in the other direction: a new audited field that nobody adds here is
// silently redacted, which is safe but hides real audit data. Keeping the two
// sets in one place makes the omission visible in review.
func TestEveryEmittedHistoryFieldIsAccountedFor(t *testing.T) {
	// Fields the store writes today, from the historyRow literals in
	// store_create.go, store_update.go, store_bulk.go, store_delete.go and
	// store_defaults.go.
	emitted := []string{
		"code", "name", "description", "type",
		"is_postable", "is_active", "is_visible",
		"attributes", "coa_account_id",
	}
	redactedOnPurpose := map[string]bool{"attributes": true}

	for _, f := range emitted {
		if redactedOnPurpose[f] {
			assert.False(t, valueSafeFields[f],
				"%q holds encrypted material and must stay redacted", f)
			continue
		}
		assert.True(t, valueSafeFields[f],
			"%q is written to history but is not allowlisted, so its values are silently redacted", f)
	}
}

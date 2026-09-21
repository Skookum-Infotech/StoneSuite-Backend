package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCustomerNotUsableMessage(t *testing.T) {
	cases := []struct {
		name       string
		code, stat string
		wantUsable bool
	}{
		{"active customer", "CACT", "Active", true},
		{"draft customer", "CDRF", "Draft", false},
		{"inactive customer", "CINA", "Inactive", false},
		{"customer on credit hold", "CCHD", "Credit Hold", false},
		{"customer with no status is left alone", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := customerNotUsableMessage(tc.code, tc.stat)
			if tc.wantUsable {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.stat, "the message names the status")
			assert.Contains(t, got, "Only Active customers")
		})
	}
}

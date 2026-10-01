package purchaseorder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanResendToVendor(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"DRFT", false}, // never sent
		{"PAPV", false}, // still awaiting approval: sending would skip it
		{"APPV", false}, // approved but "Send to Vendor" not pressed yet
		{"SENT", true},
		{"PART", true},
		{"RCVD", true},
		{"CLSD", false},
		{"CANC", false},
		{"", false},
		{"sent", false}, // codes are exact
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			assert.Equal(t, tt.want, CanResendToVendor(tt.status))
		})
	}
}

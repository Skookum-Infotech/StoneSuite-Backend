package vendorpayment

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVendorPayment_JSONDates(t *testing.T) {
	sched := "2026-09-01"
	tests := []struct {
		name          string
		scheduled     *string
		wantScheduled bool
	}{
		{"scheduled set", &sched, true},
		{"scheduled nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(VendorPayment{PaymentDate: "2026-08-24", ScheduledDate: tt.scheduled})
			require.NoError(t, err)
			var m map[string]any
			require.NoError(t, json.Unmarshal(b, &m))
			assert.Equal(t, "2026-08-24", m["paymentDate"])
			v, ok := m["scheduledDate"]
			assert.Equal(t, tt.wantScheduled, ok)
			if tt.wantScheduled {
				assert.Equal(t, "2026-09-01", v)
			}
		})
	}
}

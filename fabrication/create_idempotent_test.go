package fabrication

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateOnce_RejectsOversizedRequestID(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		wantErr   bool
	}{
		{"at the limit", strings.Repeat("a", maxCreateRequestIDLen), false},
		{"over the limit", strings.Repeat("a", maxCreateRequestIDLen+1), true},
		{"padding is trimmed first", " " + strings.Repeat("a", maxCreateRequestIDLen) + " ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Both rejections happen before the (nil) pool is touched. A blank sales
			// order is rejected first, so a valid-length key is told apart by that
			// message, and an oversized one by the requestId message.
			in := CreateJobInput{RequestID: tc.requestID}
			if tc.wantErr {
				in.SalesOrderUUID = "so-1"
			}
			_, _, err := CreateOnce(context.Background(), nil, in, 1)
			var ce ClientError
			assert.True(t, errors.As(err, &ce))
			if tc.wantErr {
				assert.Contains(t, ce.Msg, "requestId")
			} else {
				assert.Contains(t, ce.Msg, "sales order is required")
			}
		})
	}
}

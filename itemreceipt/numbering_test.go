package itemreceipt

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		name string
		id   int64
		want string
	}{
		{"first", 1, "IRCT-000001"},
		{"two digits", 42, "IRCT-000042"},
		{"exactly six digits", 999999, "IRCT-999999"},
		{"overflows the padding rather than truncating", 1000000, "IRCT-1000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatNumber(tt.id))
		})
	}
}

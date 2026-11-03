package idempotency

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashRequest(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"identical", `{"a":1}`, `{"a":1}`, true},
		{"different body", `{"a":1}`, `{"a":2}`, false},
		{"whitespace differs", `{"a":1}`, `{"a": 1}`, false},
		{"empty vs non-empty", ``, `x`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ha, hb := HashRequest([]byte(tc.a)), HashRequest([]byte(tc.b))
			assert.Len(t, ha, 64)
			assert.Equal(t, tc.same, ha == hb)
		})
	}
}

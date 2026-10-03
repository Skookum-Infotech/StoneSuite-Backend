package docextractjob

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
)

// The byte cap stays int64 end to end, so a large env value is never narrowed.
func TestConfigFromApp_MaxBytesKeptExact(t *testing.T) {
	cases := []struct {
		name string
		in   int64
	}{
		{"default", 10 << 20},
		{"above int32", math.MaxInt32 + 1},
		{"max int64", math.MaxInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConfigFromApp(config.Config{DocExtractMaxBytes: tc.in})
			assert.Equal(t, tc.in, got.Limits.MaxBytes)
		})
	}
}

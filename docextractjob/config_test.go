package docextractjob

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
)

func TestConfigFromApp_MaxBytesBounded(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int
	}{
		{"default", 10 << 20, 10 << 20},
		{"at bound", math.MaxInt32, math.MaxInt32},
		{"above bound is clamped", math.MaxInt64, math.MaxInt32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConfigFromApp(config.Config{DocExtractMaxBytes: tc.in})
			assert.Equal(t, tc.want, got.Limits.MaxBytes)
		})
	}
}

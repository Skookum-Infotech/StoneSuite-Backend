package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPercentileMillis(t *testing.T) {
	ms := func(vals ...int64) []time.Duration {
		out := make([]time.Duration, len(vals))
		for i, v := range vals {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name string
		durs []time.Duration
		p    float64
		want int64
	}{
		{name: "empty", durs: nil, p: 50, want: 0},
		{name: "single value p50", durs: ms(100), p: 50, want: 100},
		{name: "single value p90", durs: ms(100), p: 90, want: 100},
		{name: "ten values p50", durs: ms(10, 20, 30, 40, 50, 60, 70, 80, 90, 100), p: 50, want: 50},
		{name: "ten values p90", durs: ms(10, 20, 30, 40, 50, 60, 70, 80, 90, 100), p: 90, want: 90},
		{name: "unsorted input", durs: ms(100, 10, 50, 20), p: 50, want: 20},
		{name: "p100 is max", durs: ms(5, 1, 9), p: 100, want: 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, percentileMillis(tc.durs, tc.p))
		})
	}
}

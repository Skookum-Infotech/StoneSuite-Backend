package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const testGiB = float64(1 << 30)

func TestFleetP95(t *testing.T) {
	seq := func(n int) []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = float64(n - i) // descending, to prove sorting
		}
		return s
	}
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"nil", nil, 0},
		{"empty", []float64{}, 0},
		{"single", []float64{7}, 7},
		{"two", []float64{1, 2}, 2},
		{"ten", seq(10), 10},
		{"twenty", seq(20), 19},
		{"hundred", seq(100), 95},
		{"twenty one", seq(21), 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FleetP95(tt.in))
		})
	}
	in := []float64{3, 1, 2}
	FleetP95(in)
	assert.Equal(t, []float64{3, 1, 2}, in, "input must not be reordered")
}

func TestFormatBytesGB(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0.0 GB"},
		{1.5 * testGiB, "1.5 GB"},
		{99.94 * testGiB, "99.9 GB"},
		{99.96 * testGiB, "100 GB"},
		{100 * testGiB, "100 GB"},
		{410 * testGiB, "410 GB"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, formatBytesGB(tt.in))
	}
}

func TestFormatCount(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1,000"}, {3100, "3,100"}, {1234567, "1,234,567"}, {-4200, "-4,200"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, formatCount(tt.in))
	}
}

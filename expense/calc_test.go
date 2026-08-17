package expense

import "testing"

func TestComputeHeaderTotal(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"basic", []float64{412.50, 38.20}, 450.70},
		{"single", []float64{100}, 100},
		{"empty", nil, 0},
		{"zero amounts", []float64{0, 0}, 0},
		{"many lines", []float64{10, 20, 30.55, 0.45}, 61},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeHeaderTotal(tt.in); got != tt.want {
				t.Errorf("ComputeHeaderTotal(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

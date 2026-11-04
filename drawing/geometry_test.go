package drawing

import (
	"math"
	"testing"
)

func TestPolygonAreaMm2(t *testing.T) {
	cases := []struct {
		name    string
		outline []Point
		want    float64
	}{
		{"600x400 rect, clockwise", []Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: -400}, {X: 0, Y: -400}}, 240000},
		{"same rect, counter-clockwise", []Point{{X: 0, Y: 0}, {X: 0, Y: -400}, {X: 600, Y: -400}, {X: 600, Y: 0}}, 240000},
		{"triangle", []Point{{X: 0, Y: 0}, {X: 300, Y: 0}, {X: 0, Y: 300}}, 45000},
		{"degenerate: 2 points", []Point{{X: 0, Y: 0}, {X: 600, Y: 0}}, 0},
		{"degenerate: 0 points", nil, 0},
		{"collinear 3 points (zero area)", []Point{{X: 0, Y: 0}, {X: 100, Y: 0}, {X: 200, Y: 0}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := polygonAreaMm2(c.outline)
			if math.Abs(got-c.want) > 0.01 {
				t.Errorf("polygonAreaMm2() = %v, want %v", got, c.want)
			}
		})
	}
}

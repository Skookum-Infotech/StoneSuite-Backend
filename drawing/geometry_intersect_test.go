package drawing

import "testing"

func TestOutlineSelfIntersects(t *testing.T) {
	cases := []struct {
		name    string
		outline []Point
		want    bool
	}{
		{"rectangle", []Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: -400}, {X: 0, Y: -400}}, false},
		{"L shape", []Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: -200}, {X: 300, Y: -200}, {X: 300, Y: -400}, {X: 0, Y: -400}}, false},
		{"bow-tie", []Point{{X: 0, Y: 0}, {X: 100, Y: -100}, {X: 100, Y: 0}, {X: 0, Y: -60}}, true},
		{"edge folds back over its neighbour", []Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 5, Y: 0}, {X: 5, Y: -5}}, true},
		{"closing edge folds back", []Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: -10}, {X: 5, Y: 0}}, true},
		{"triangle", []Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 5, Y: -8}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := outlineSelfIntersects(c.outline); got != c.want {
				t.Errorf("outlineSelfIntersects() = %v, want %v", got, c.want)
			}
		})
	}
}

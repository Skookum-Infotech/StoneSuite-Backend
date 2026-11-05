package drawing

// polygonAreaMm2 returns the unsigned area, in square millimetres, of the
// polygon formed by outline (implicitly closed, same convention as the
// frontend's lib/drawing/geometry.ts area()). Fewer than 3 points, or
// collinear points, yield 0.
func polygonAreaMm2(outline []Point) float64 {
	if len(outline) < 3 {
		return 0
	}
	var signed float64
	for i := range outline {
		j := (i + 1) % len(outline)
		signed += outline[i].X*outline[j].Y - outline[j].X*outline[i].Y
	}
	if signed < 0 {
		signed = -signed
	}
	return signed / 2
}

func cross(o, a, b Point) float64 {
	return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X)
}

func within(p, a, b Point) bool {
	return minF(a.X, b.X) <= p.X && p.X <= maxF(a.X, b.X) && minF(a.Y, b.Y) <= p.Y && p.Y <= maxF(a.Y, b.Y)
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// segmentsTouch reports whether the closed segments a-b and c-d share a point.
func segmentsTouch(a, b, c, d Point) bool {
	d1, d2 := cross(c, d, a), cross(c, d, b)
	d3, d4 := cross(a, b, c), cross(a, b, d)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	return (d1 == 0 && within(a, c, d)) || (d2 == 0 && within(b, c, d)) ||
		(d3 == 0 && within(c, a, b)) || (d4 == 0 && within(d, a, b))
}

// outlineSelfIntersects reports whether any two non-adjacent sides of the
// closed outline touch, or two adjacent sides fold back over each other. Same
// rule as the frontend's isSimplePolygon (src/lib/drawing/polygon.ts).
func outlineSelfIntersects(outline []Point) bool {
	n := len(outline)
	for i := 0; i < n; i++ {
		a, b := outline[i], outline[(i+1)%n]
		for j := i + 1; j < n; j++ {
			c, d := outline[j], outline[(j+1)%n]
			adjacent := j == i+1 || (i == 0 && j == n-1)
			if !adjacent {
				if segmentsTouch(a, b, c, d) {
					return true
				}
				continue
			}
			// Adjacent sides p->q, q->r share only q; reject a fold-back.
			p, q, r := a, b, d
			if j == n-1 && i == 0 {
				p, q, r = c, d, b
			}
			dot := (q.X-p.X)*(r.X-q.X) + (q.Y-p.Y)*(r.Y-q.Y)
			if cross(p, q, r) == 0 && dot < 0 {
				return true
			}
		}
	}
	return false
}

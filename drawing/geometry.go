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

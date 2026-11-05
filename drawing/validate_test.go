package drawing

import "testing"

var validRectOutline = []Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: -400}, {X: 0, Y: -400}}

func validPiece(id string) Piece {
	return Piece{ID: id, Label: id, Shape: "rect", Outline: validRectOutline, ThicknessMm: 30}
}

func f(v float64) *float64 { return &v }

func withRotation(p Piece, deg float64) Piece {
	p.RotationDeg = deg
	return p
}

func withCutout(c Cutout) Piece {
	p := validPiece("p1")
	p.Cutouts = []Cutout{c}
	return p
}

func nPieces(n int) []Piece {
	pieces := make([]Piece, n)
	for i := range pieces {
		pieces[i] = validPiece("p")
	}
	return pieces
}

func nAreasOfMPieces(n, m int) []Area {
	areas := make([]Area, n)
	for i := range areas {
		areas[i] = Area{ID: "a", Pieces: nPieces(m)}
	}
	return areas
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		drawing Drawing
		wantErr bool
	}{
		{"valid single rect piece", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{validPiece("p1")}}}}, false},
		{"no areas at all is valid (blank drawing)", Drawing{Areas: nil}, false},
		{"piece with 2-point outline rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{{ID: "p1", Outline: []Point{{X: 0, Y: 0}, {X: 1, Y: 1}}}}}}}, true},
		{"zero-area (collinear) outline rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{{ID: "p1", Outline: []Point{{X: 0, Y: 0}, {X: 100, Y: 0}, {X: 200, Y: 0}}}}}}}, true},
		{"201 pieces in one area rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: nPieces(201)}}}, true},
		{"200 pieces in one area accepted (at the cap)", Drawing{Areas: []Area{{ID: "a1", Pieces: nPieces(200)}}}, false},
		{"21 areas rejected", Drawing{Areas: nAreasOfMPieces(21, 1)}, true},
		{"20 areas accepted (at the cap)", Drawing{Areas: nAreasOfMPieces(20, 1)}, false},
		{"bow-tie outline rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{{ID: "p1", ThicknessMm: 30, Outline: []Point{{X: 0, Y: 0}, {X: 100, Y: -100}, {X: 100, Y: 0}, {X: 0, Y: -60}}}}}}}, true},
		{"L-shaped outline accepted", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{{ID: "p1", ThicknessMm: 30, Outline: []Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: -200}, {X: 300, Y: -200}, {X: 300, Y: -400}, {X: 0, Y: -400}}}}}}}, false},
		{"zero thickness rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{{ID: "p1", Outline: validRectOutline}}}}}, true},
		{"free rotation accepted", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withRotation(validPiece("p1"), 45.5)}}}}, false},
		{"rect sink accepted", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "sink", Shape: "rect", WidthMm: f(300), DepthMm: f(200)})}}}}, false},
		{"round faucet accepted", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "faucet", Shape: "round", DiameterMm: f(35)})}}}}, false},
		{"custom polygon accepted", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "custom", Shape: "polygon", Outline: []Point{{X: -10, Y: 10}, {X: 10, Y: 10}, {X: 0, Y: -10}}})}}}}, false},
		{"legacy outlet with only a diameter accepted", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "outlet", DiameterMm: f(30)})}}}}, false},
		{"unknown cutout type rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "jacuzzi", WidthMm: f(1), DepthMm: f(1)})}}}}, true},
		{"unknown cutout shape rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "sink", Shape: "star", WidthMm: f(1), DepthMm: f(1)})}}}}, true},
		{"rect cutout with no size rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "sink", Shape: "rect"})}}}}, true},
		{"round cutout with zero diameter rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "faucet", Shape: "round", DiameterMm: f(0)})}}}}, true},
		{"polygon cutout with 2 points rejected", Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{withCutout(Cutout{ID: "c", Type: "custom", Shape: "polygon", Outline: []Point{{X: 0, Y: 0}, {X: 1, Y: 1}}})}}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.drawing)
			if (err != nil) != c.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

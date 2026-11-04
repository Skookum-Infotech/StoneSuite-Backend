package drawing

import "testing"

var validRectOutline = []Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: -400}, {X: 0, Y: -400}}

func validPiece(id string) Piece {
	return Piece{ID: id, Label: id, Shape: "rect", Outline: validRectOutline, ThicknessMm: 30}
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

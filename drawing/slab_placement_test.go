package drawing

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The Material tab lays a piece on a slab. The placement must survive a save and
// reload, or the slab layout vanishes the next time the order is opened.
func TestPieceSlabPlacementJSONRoundTrip(t *testing.T) {
	in := `{
		"id":"p1","label":"A-1","shape":"rect",
		"outline":[{"x":0,"y":0},{"x":600,"y":0},{"x":600,"y":-400},{"x":0,"y":-400}],
		"position":{"x":0,"y":0},"rotationDeg":0,"thicknessMm":30,
		"edges":[],"seams":[],"cutouts":[],
		"slab":{"slabId":"7d1c4a52-3a0e-4f4c-9a53-0d2b7a1c9e11","x":120.5,"y":80,"rotationDeg":90}
	}`
	var piece Piece
	if err := json.Unmarshal([]byte(in), &piece); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(piece)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip lost or changed fields:\nwant %v\n got %v", want, got)
	}
}

// A piece that was never placed must not grow a "slab" key.
func TestPieceWithoutSlabOmitsTheKey(t *testing.T) {
	out, err := json.Marshal(validPiece("p1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"slab"`) {
		t.Errorf("an unplaced piece should not carry a slab key: %s", out)
	}
}

func TestValidateSlabPlacement(t *testing.T) {
	placed := func(s *SlabPlacement) Drawing {
		p := validPiece("p1")
		p.Slab = s
		return Drawing{Areas: []Area{{ID: "a1", Pieces: []Piece{p}}}}
	}
	cases := []struct {
		name    string
		drawing Drawing
		wantErr bool
	}{
		{"no placement accepted", placed(nil), false},
		{"placement on a slab accepted", placed(&SlabPlacement{SlabID: "7d1c4a52-3a0e-4f4c-9a53-0d2b7a1c9e11", X: 10, Y: 20, RotationDeg: 90}), false},
		{"placement without a slab id rejected", placed(&SlabPlacement{X: 10, Y: 20}), true},
		{"blank slab id rejected", placed(&SlabPlacement{SlabID: "   "}), true},
		{"overlong slab id rejected", placed(&SlabPlacement{SlabID: strings.Repeat("x", maxSlabIDLen+1)}), true},
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

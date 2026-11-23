package drawing

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func withEdges(edges ...PieceEdge) Piece {
	p := validPiece("p1")
	p.Edges = edges
	return p
}

func oneArea(pieces []Piece, notes []Note, dims []Dimension) Drawing {
	return Drawing{Areas: []Area{{ID: "a1", Pieces: pieces, Notes: notes, Dimensions: dims}}}
}

func TestValidateEdgesAndAnnotations(t *testing.T) {
	cases := []struct {
		name    string
		drawing Drawing
		wantErr bool
	}{
		{"edge profile, overhang and attached profile accepted",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, Code: "EE", OverhangMm: f(25),
				AttachedProfile: &AttachedProfile{ProfileTypeID: "backsplash-1", MeasurementMm: 100}})}, nil, nil), false},
		{"edge on a side that does not exist rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 4, Code: "EE"})}, nil, nil), true},
		{"negative edge index rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: -1, Code: "EE"})}, nil, nil), true},
		{"two entries for one side rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 1, Code: "EE"}, PieceEdge{Index: 1, Code: "BN"})}, nil, nil), true},
		{"negative overhang rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, OverhangMm: f(-1)})}, nil, nil), true},
		{"empty attached profile type id rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, AttachedProfile: &AttachedProfile{ProfileTypeID: "", MeasurementMm: 100}})}, nil, nil), true},
		{"zero attached profile measurement rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, AttachedProfile: &AttachedProfile{ProfileTypeID: "backsplash-1", MeasurementMm: 0}})}, nil, nil), true},
		{"absurd attached profile measurement rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, AttachedProfile: &AttachedProfile{ProfileTypeID: "backsplash-1", MeasurementMm: 1e9}})}, nil, nil), true},
		{"attached profile placed on a slab accepted",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, AttachedProfile: &AttachedProfile{
				ProfileTypeID: "backsplash-1", MeasurementMm: 100,
				Slab: &SlabPlacement{SlabID: "7d1c4a52-3a0e-4f4c-9a53-0d2b7a1c9e11", X: 10, Y: 20, RotationDeg: 90},
			}})}, nil, nil), false},
		{"attached profile placed on a slab without a slab id rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, AttachedProfile: &AttachedProfile{
				ProfileTypeID: "backsplash-1", MeasurementMm: 100, Slab: &SlabPlacement{X: 10, Y: 20},
			}})}, nil, nil), true},
		{"overlong edge code rejected",
			oneArea([]Piece{withEdges(PieceEdge{Index: 0, Code: strings.Repeat("X", 17)})}, nil, nil), true},
		{"note within the length limit accepted",
			oneArea(nil, []Note{{ID: "n", Text: strings.Repeat("a", 500)}}, nil), false},
		{"note over the length limit rejected",
			oneArea(nil, []Note{{ID: "n", Text: strings.Repeat("a", 501)}}, nil), true},
		{"dimension between two points accepted",
			oneArea(nil, nil, []Dimension{{ID: "d", From: Point{X: 0, Y: 0}, To: Point{X: 100, Y: 0}, OffsetMm: 40}}), false},
		{"dimension between identical points rejected",
			oneArea(nil, nil, []Dimension{{ID: "d", From: Point{X: 5, Y: 5}, To: Point{X: 5, Y: 5}}}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Validate(c.drawing); (err != nil) != c.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestAreaJSONRoundTripKeepsEdgesAndAnnotations(t *testing.T) {
	in := `{
		"id":"a1","name":"Kitchen","code":"K","sortOrder":0,"cabinets":[],
		"pieces":[{"id":"p1","label":"A-1","shape":"rect","outline":[{"x":0,"y":0},{"x":600,"y":0},{"x":600,"y":-400},{"x":0,"y":-400}],
			"position":{"x":0,"y":0},"rotationDeg":0,"thicknessMm":30,"seams":[],"cutouts":[],
			"edges":[{"index":0,"code":"EE","overhangMm":25,"attachedProfile":{"profileTypeId":"backsplash-1","measurementMm":100}}]}],
		"notes":[{"id":"n1","text":"Seam here","at":{"x":1,"y":2},"pieceId":"p1"}],
		"dimensions":[{"id":"d1","from":{"x":0,"y":0},"to":{"x":100,"y":0},"offsetMm":40}]
	}`
	var area Area
	if err := json.Unmarshal([]byte(in), &area); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(area)
	if err != nil {
		t.Fatal(err)
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

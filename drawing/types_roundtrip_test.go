package drawing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A saved drawing must come back from the database exactly as the editor sent
// it. encoding/json drops fields it has no struct field for, so any field the
// frontend adds but this package forgets would vanish on the next save —
// this round-trips every optional field the editor currently writes.
func TestPieceJSONRoundTripKeepsEveryField(t *testing.T) {
	in := `{
		"id":"p1","label":"A-1","shape":"rect","kind":"island",
		"outline":[{"x":0,"y":0},{"x":600,"y":0},{"x":600,"y":-400},{"x":0,"y":-400}],
		"position":{"x":10.5,"y":-20.25},"rotationDeg":45.5,"thicknessMm":30,
		"edges":[],"seams":[],
		"cutouts":[{
			"id":"c1","type":"custom","shape":"polygon","center":{"x":300,"y":-200},
			"widthMm":100,"depthMm":80,"diameterMm":35,"rotationDeg":12.5,
			"outline":[{"x":-10,"y":10},{"x":10,"y":10},{"x":0,"y":-10}],
			"inventoryItemId":"item-9","edgeCode":"EE","label":"Prep sink"
		}]
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

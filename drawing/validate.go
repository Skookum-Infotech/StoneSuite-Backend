package drawing

import "fmt"

// Spec §6 "Backend contract" caps: closed outlines, positive area,
// <=200 pieces per area, <=20 areas.
const (
	maxAreasPerDrawing = 20
	maxPiecesPerArea   = 200
	maxCutoutsPerPiece = 100
	minOutlinePoints   = 3
)

// allowedCutoutTypes / allowedCutoutShapes mirror the frontend's CutoutType and
// CutoutShape unions (src/types/drawing.ts). An empty shape is allowed: it is
// what a cutout saved before `shape` existed looks like.
var (
	allowedCutoutTypes  = map[string]bool{"sink": true, "cooktop": true, "faucet": true, "soap_dispenser": true, "outlet": true, "custom": true}
	allowedCutoutShapes = map[string]bool{"": true, "rect": true, "round": true, "polygon": true}
)

// Validate checks a Drawing against the spec's server-side invariants and
// returns the first violation found, wrapped with enough context to show
// the caller which piece/area failed.
//
// Where a cutout sits inside its piece is checked by the editor (it needs the
// clearance rules the shop tunes there); the server only guarantees the
// document is well-formed: sane outlines, positive thickness, and cutouts of
// a known type with a usable size.
func Validate(d Drawing) error {
	if len(d.Areas) > maxAreasPerDrawing {
		return fmt.Errorf("drawing: %d areas exceeds the %d-area limit", len(d.Areas), maxAreasPerDrawing)
	}
	for _, area := range d.Areas {
		if len(area.Pieces) > maxPiecesPerArea {
			return fmt.Errorf("drawing: area %q has %d pieces, exceeds the %d-piece limit", area.ID, len(area.Pieces), maxPiecesPerArea)
		}
		for _, piece := range area.Pieces {
			if err := validatePiece(piece); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePiece(piece Piece) error {
	if len(piece.Outline) < minOutlinePoints {
		return fmt.Errorf("drawing: piece %q has an outline with fewer than %d points", piece.ID, minOutlinePoints)
	}
	if polygonAreaMm2(piece.Outline) <= 0 {
		return fmt.Errorf("drawing: piece %q has a closed outline with zero or negative area", piece.ID)
	}
	if outlineSelfIntersects(piece.Outline) {
		return fmt.Errorf("drawing: piece %q has an outline whose sides cross each other", piece.ID)
	}
	if piece.ThicknessMm <= 0 {
		return fmt.Errorf("drawing: piece %q needs a thickness above zero", piece.ID)
	}
	if len(piece.Cutouts) > maxCutoutsPerPiece {
		return fmt.Errorf("drawing: piece %q has %d cutouts, exceeds the %d-cutout limit", piece.ID, len(piece.Cutouts), maxCutoutsPerPiece)
	}
	for _, cutout := range piece.Cutouts {
		if err := validateCutout(piece.ID, cutout); err != nil {
			return err
		}
	}
	return nil
}

func positive(v *float64) bool { return v != nil && *v > 0 }

func validateCutout(pieceID string, c Cutout) error {
	if !allowedCutoutTypes[c.Type] {
		return fmt.Errorf("drawing: piece %q cutout %q has an unknown type %q", pieceID, c.ID, c.Type)
	}
	if !allowedCutoutShapes[c.Shape] {
		return fmt.Errorf("drawing: piece %q cutout %q has an unknown shape %q", pieceID, c.ID, c.Shape)
	}
	switch {
	case c.Shape == "round":
		if !positive(c.DiameterMm) {
			return fmt.Errorf("drawing: piece %q cutout %q needs a diameter above zero", pieceID, c.ID)
		}
	case c.Shape == "polygon":
		if len(c.Outline) < minOutlinePoints || polygonAreaMm2(c.Outline) <= 0 {
			return fmt.Errorf("drawing: piece %q cutout %q needs a custom outline with area", pieceID, c.ID)
		}
	default:
		// "rect", or a legacy cutout with no shape: sized by width x depth, or a
		// bare diameter (what the original data model offered for outlets).
		if !(positive(c.WidthMm) && positive(c.DepthMm)) && !positive(c.DiameterMm) {
			return fmt.Errorf("drawing: piece %q cutout %q needs a width and depth above zero", pieceID, c.ID)
		}
	}
	return nil
}

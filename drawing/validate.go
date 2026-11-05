package drawing

import (
	"fmt"
	"math"
	"unicode/utf8"
)

// Spec §6 "Backend contract" caps: closed outlines, positive area,
// <=200 pieces per area, <=20 areas.
const (
	maxAreasPerDrawing = 20
	maxPiecesPerArea   = 200
	maxCutoutsPerPiece = 100
	minOutlinePoints   = 3
	maxNotesPerArea    = 200
	maxDimsPerArea     = 200
	maxNoteTextRunes   = 500
	maxEdgeCodeRunes   = 16
	// maxEdgeHeightMm bounds overhang / backsplash / waterfall heights; it is
	// the frontend's MAX_DIMENSION_MM.
	maxEdgeHeightMm = 30000.0
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
		if err := validateAnnotations(area); err != nil {
			return err
		}
	}
	return nil
}

func validateAnnotations(area Area) error {
	if len(area.Notes) > maxNotesPerArea {
		return fmt.Errorf("drawing: area %q has %d notes, exceeds the %d-note limit", area.ID, len(area.Notes), maxNotesPerArea)
	}
	for _, n := range area.Notes {
		if utf8.RuneCountInString(n.Text) > maxNoteTextRunes {
			return fmt.Errorf("drawing: area %q note %q is longer than %d characters", area.ID, n.ID, maxNoteTextRunes)
		}
	}
	if len(area.Dimensions) > maxDimsPerArea {
		return fmt.Errorf("drawing: area %q has %d dimensions, exceeds the %d-dimension limit", area.ID, len(area.Dimensions), maxDimsPerArea)
	}
	for _, d := range area.Dimensions {
		if d.From == d.To {
			return fmt.Errorf("drawing: area %q dimension %q measures between identical points", area.ID, d.ID)
		}
	}
	return nil
}

func validateEdges(piece Piece) error {
	seen := map[int]bool{}
	for _, e := range piece.Edges {
		if e.Index < 0 || e.Index >= len(piece.Outline) {
			return fmt.Errorf("drawing: piece %q has edge settings for side %d, which does not exist", piece.ID, e.Index)
		}
		if seen[e.Index] {
			return fmt.Errorf("drawing: piece %q has two sets of edge settings for side %d", piece.ID, e.Index)
		}
		seen[e.Index] = true
		if utf8.RuneCountInString(e.Code) > maxEdgeCodeRunes {
			return fmt.Errorf("drawing: piece %q side %d has an edge code longer than %d characters", piece.ID, e.Index, maxEdgeCodeRunes)
		}
		for name, v := range map[string]*float64{"overhang": e.OverhangMm, "backsplash height": e.SplashHeightMm, "waterfall height": e.WaterfallHeightMm} {
			if v != nil && (*v < 0 || *v > maxEdgeHeightMm || math.IsNaN(*v)) {
				return fmt.Errorf("drawing: piece %q side %d has an invalid %s", piece.ID, e.Index, name)
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
	if err := validateEdges(piece); err != nil {
		return err
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
	switch c.Shape {
	case "round":
		if !positive(c.DiameterMm) {
			return fmt.Errorf("drawing: piece %q cutout %q needs a diameter above zero", pieceID, c.ID)
		}
	case "polygon":
		if len(c.Outline) < minOutlinePoints || polygonAreaMm2(c.Outline) <= 0 {
			return fmt.Errorf("drawing: piece %q cutout %q needs a custom outline with area", pieceID, c.ID)
		}
	default:
		// "rect", or a legacy cutout with no shape: sized by width x depth, or a
		// bare diameter (what the original data model offered for outlets).
		if (!positive(c.WidthMm) || !positive(c.DepthMm)) && !positive(c.DiameterMm) {
			return fmt.Errorf("drawing: piece %q cutout %q needs a width and depth above zero", pieceID, c.ID)
		}
	}
	return nil
}

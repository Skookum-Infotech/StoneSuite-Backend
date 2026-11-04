package drawing

import "fmt"

// Spec §6 "Backend contract" caps: closed outlines, positive area,
// <=200 pieces per area, <=20 areas.
const (
	maxAreasPerDrawing = 20
	maxPiecesPerArea   = 200
	minOutlinePoints   = 3
)

// Validate checks a Drawing against the spec's server-side invariants and
// returns the first violation found, wrapped with enough context to show
// the caller which piece/area failed.
func Validate(d Drawing) error {
	if len(d.Areas) > maxAreasPerDrawing {
		return fmt.Errorf("drawing: %d areas exceeds the %d-area limit", len(d.Areas), maxAreasPerDrawing)
	}
	for _, area := range d.Areas {
		if len(area.Pieces) > maxPiecesPerArea {
			return fmt.Errorf("drawing: area %q has %d pieces, exceeds the %d-piece limit", area.ID, len(area.Pieces), maxPiecesPerArea)
		}
		for _, piece := range area.Pieces {
			if len(piece.Outline) < minOutlinePoints {
				return fmt.Errorf("drawing: piece %q has an outline with fewer than %d points", piece.ID, minOutlinePoints)
			}
			if polygonAreaMm2(piece.Outline) <= 0 {
				return fmt.Errorf("drawing: piece %q has a closed outline with zero or negative area", piece.ID)
			}
		}
	}
	return nil
}

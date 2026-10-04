package fabrication

import (
	"math"
	"strings"
)

// MaterialSurface describes the usable rectangular bounds of a physical unit.
// Remnant outlines, defects and grain still require an operator's layout review.
type MaterialSurface struct {
	MaterialID                     string
	Finish                         string
	LengthMM, WidthMM, ThicknessMM float64
}

// PiecePlacement locates a measured piece within its slab, in millimetres.
// PieceIndex refers to the immutable template line's zero-based piece index.
type PiecePlacement struct {
	PieceIndex int     `json:"pieceIndex"`
	XMM        float64 `json:"xMm"`
	YMM        float64 `json:"yMm"`
	Rotated    bool    `json:"rotated"`
}

// MaterialLayout records explicit placement and an operator's suitability review.
// A subset of a line may be assigned to one slab; remaining pieces need other slabs.
type MaterialLayout struct {
	Placements           []PiecePlacement `json:"placements"`
	KerfMM               float64          `json:"kerfMm"`
	SuitabilityConfirmed bool             `json:"suitabilityConfirmed"`
	ReviewNote           string           `json:"reviewNote"`
}

type layoutRectangle struct{ x, y, length, width float64 }

// ValidateMaterialLayout checks compatibility, bounds and separation without
// pretending to solve nesting or infer suitability from total area alone.
func ValidateMaterialLayout(surface MaterialSurface, line TemplateLine, layout MaterialLayout) error {
	if surface.MaterialID == "" || surface.MaterialID != line.MaterialID {
		return ClientError{Msg: "Choose the material specified by the approved template."}
	}
	if line.Finish != "" && !strings.EqualFold(strings.TrimSpace(surface.Finish), strings.TrimSpace(line.Finish)) {
		return ClientError{Msg: "The slab finish does not match the approved template."}
	}
	for _, dimension := range []float64{surface.LengthMM, surface.WidthMM, surface.ThicknessMM} {
		if !positiveFinite(dimension) || dimension > maxMeasuredDimensionMM {
			return ClientError{Msg: "The slab needs valid measured dimensions."}
		}
	}
	if !layout.SuitabilityConfirmed || strings.TrimSpace(layout.ReviewNote) == "" {
		return ClientError{Msg: "Confirm layout suitability and record the grain, finish and defect review."}
	}
	if len(layout.Placements) == 0 || !nonnegativeFinite(layout.KerfMM) || layout.KerfMM > maxMeasuredDimensionMM {
		return ClientError{Msg: "Choose measured pieces and provide a valid saw kerf."}
	}
	seen := make(map[int]bool, len(layout.Placements))
	rectangles := make([]layoutRectangle, 0, len(layout.Placements))
	for _, placement := range layout.Placements {
		if placement.PieceIndex < 0 || placement.PieceIndex >= len(line.Pieces) || seen[placement.PieceIndex] {
			return ClientError{Msg: "Each placement must identify a distinct measured piece."}
		}
		seen[placement.PieceIndex] = true
		piece := line.Pieces[placement.PieceIndex]
		for _, dimension := range []float64{piece.LengthMM, piece.WidthMM, piece.ThicknessMM} {
			if !positiveFinite(dimension) || dimension > maxMeasuredDimensionMM {
				return ClientError{Msg: "The template piece needs valid measured dimensions."}
			}
		}
		if piece.ThicknessMM != surface.ThicknessMM {
			return ClientError{Msg: "The slab thickness does not match the measured piece."}
		}
		if !nonnegativeFinite(placement.XMM) || !nonnegativeFinite(placement.YMM) {
			return ClientError{Msg: "Piece positions must be finite and inside the slab."}
		}
		rect := layoutRectangle{placement.XMM, placement.YMM, piece.LengthMM, piece.WidthMM}
		if placement.Rotated {
			rect.length, rect.width = rect.width, rect.length
		}
		if rect.x+rect.length > surface.LengthMM || rect.y+rect.width > surface.WidthMM {
			return ClientError{Msg: "A measured piece extends beyond the slab dimensions."}
		}
		for _, other := range rectangles {
			if !(rect.x+rect.length+layout.KerfMM <= other.x || other.x+other.length+layout.KerfMM <= rect.x || rect.y+rect.width+layout.KerfMM <= other.y || other.y+other.width+layout.KerfMM <= rect.y) {
				return ClientError{Msg: "Piece placements overlap or leave insufficient saw kerf."}
			}
		}
		rectangles = append(rectangles, rect)
	}
	return nil
}

func nonnegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

package fabrication

import (
	"math"
	"strings"
)

const maxMeasuredDimensionMM = 100000

// CompareTemplate requires internal review for every submission and customer
// review for commercial changes, independently of client-provided flags.
func CompareTemplate(baseline, proposed []TemplateLine) (TemplateChange, error) {
	out := TemplateChange{InternalRequired: true, ChangedLines: []string{}}
	if len(proposed) == 0 {
		return out, ClientError{Msg: "A template needs at least one measured line."}
	}
	old := make(map[string]TemplateLine, len(baseline))
	for _, line := range baseline {
		old[line.SourceLineID] = line
	}
	seen := make(map[string]bool, len(proposed))
	for _, line := range proposed {
		if strings.TrimSpace(line.SourceLineID) == "" || strings.TrimSpace(line.MaterialID) == "" || seen[line.SourceLineID] {
			return out, ClientError{Msg: "Each template line needs a unique source line and material."}
		}
		seen[line.SourceLineID] = true
		if !positiveFinite(line.Quantity) || math.IsNaN(line.UnitPrice) || math.IsInf(line.UnitPrice, 0) || line.UnitPrice < 0 {
			return out, ClientError{Msg: "Quantity must be positive and price cannot be negative."}
		}
		if len(line.Pieces) == 0 {
			return out, ClientError{Msg: "Each template line needs measured pieces."}
		}
		for _, piece := range line.Pieces {
			if strings.TrimSpace(piece.Name) == "" {
				return out, ClientError{Msg: "Every measured piece needs a name."}
			}
			for _, dim := range []float64{piece.LengthMM, piece.WidthMM, piece.ThicknessMM} {
				if !positiveFinite(dim) || dim > maxMeasuredDimensionMM {
					return out, ClientError{Msg: "Piece dimensions must be positive and at most 100000 mm."}
				}
			}
		}
		before, ok := old[line.SourceLineID]
		if !ok || before.MaterialID != line.MaterialID || before.Finish != line.Finish || before.Quantity != line.Quantity || before.UnitPrice != line.UnitPrice || before.Scope != line.Scope {
			out.CustomerRequired = true
			out.ChangedLines = append(out.ChangedLines, line.SourceLineID)
		}
	}
	for _, line := range baseline {
		if !seen[line.SourceLineID] {
			out.CustomerRequired = true
			out.ChangedLines = append(out.ChangedLines, line.SourceLineID)
		}
	}
	return out, nil
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

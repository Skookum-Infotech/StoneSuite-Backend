package fabrication

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestValidateMaterialLayout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*MaterialSurface, *TemplateLine, *MaterialLayout)
		valid  bool
	}{
		{"fits", func(*MaterialSurface, *TemplateLine, *MaterialLayout) {}, true},
		{"wrong material", func(s *MaterialSurface, _ *TemplateLine, _ *MaterialLayout) { s.MaterialID = "other" }, false},
		{"wrong finish", func(s *MaterialSurface, _ *TemplateLine, _ *MaterialLayout) { s.Finish = "honed" }, false},
		{"wrong thickness", func(s *MaterialSurface, _ *TemplateLine, _ *MaterialLayout) { s.ThicknessMM = 20 }, false},
		{"too long despite enough area", func(_ *MaterialSurface, l *TemplateLine, _ *MaterialLayout) {
			l.Pieces[0].LengthMM = 3100
			l.Pieces[0].WidthMM = 100
		}, false},
		{"overlap", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements[1].XMM = 500 }, false},
		{"kerf clearance", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements[1].XMM = 1002 }, false},
		{"unreviewed", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.SuitabilityConfirmed = false }, false},
		{"no evidence", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.ReviewNote = " " }, false},
		{"duplicate piece", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements[1].PieceIndex = 0 }, false},
		{"unknown piece", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements[1].PieceIndex = 2 }, false},
		{"negative origin", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements[0].XMM = -1 }, false},
		{"nonfinite origin", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements[0].XMM = math.NaN() }, false},
		{"nonfinite slab", func(s *MaterialSurface, _ *TemplateLine, _ *MaterialLayout) { s.LengthMM = math.Inf(1) }, false},
		{"no placements", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements = nil }, false},
		{"invalid kerf", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.KerfMM = -1 }, false},
		{"partial line", func(_ *MaterialSurface, _ *TemplateLine, l *MaterialLayout) { l.Placements = l.Placements[:1] }, true},
		{"rotation fits", func(s *MaterialSurface, l *TemplateLine, p *MaterialLayout) {
			s.LengthMM = 800
			s.WidthMM = 1200
			l.Pieces = l.Pieces[:1]
			p.Placements = p.Placements[:1]
			p.Placements[0].Rotated = true
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slab := MaterialSurface{MaterialID: "stone", Finish: "polished", LengthMM: 3000, WidthMM: 1500, ThicknessMM: 30}
			line := TemplateLine{MaterialID: "stone", Finish: "polished", Pieces: []MeasuredPiece{{Name: "A", LengthMM: 1000, WidthMM: 600, ThicknessMM: 30}, {Name: "B", LengthMM: 1000, WidthMM: 600, ThicknessMM: 30}}}
			layout := MaterialLayout{KerfMM: 3, SuitabilityConfirmed: true, ReviewNote: "Grain direction and defects reviewed", Placements: []PiecePlacement{{PieceIndex: 0}, {PieceIndex: 1, XMM: 1003}}}
			tc.change(&slab, &line, &layout)
			err := ValidateMaterialLayout(slab, line, layout)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

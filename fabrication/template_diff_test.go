package fabrication

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestTemplateApprovalClassification(t *testing.T) {
	base := TemplateLine{SourceLineID: "line", MaterialID: "stone", Quantity: 10, UnitPrice: 25, Scope: "counter", Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 2000, WidthMM: 900, ThicknessMM: 30}}}
	for _, tc := range []struct {
		name     string
		mutate   func(*TemplateLine)
		customer bool
	}{
		{"unchanged", func(*TemplateLine) {}, false},
		{"measurement", func(l *TemplateLine) {
			l.Pieces = []MeasuredPiece{{Name: "Island", LengthMM: 2100, WidthMM: 900, ThicknessMM: 30}}
		}, false},
		{"material", func(l *TemplateLine) { l.MaterialID = "other" }, true},
		{"finish", func(l *TemplateLine) { l.Finish = "honed" }, true},
		{"quantity", func(l *TemplateLine) { l.Quantity = 11 }, true},
		{"price", func(l *TemplateLine) { l.UnitPrice = 26 }, true},
		{"scope", func(l *TemplateLine) { l.Scope = "counter and backsplash" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := base
			tc.mutate(&after)
			got, err := CompareTemplate([]TemplateLine{base}, []TemplateLine{after})
			require.NoError(t, err)
			require.True(t, got.InternalRequired)
			require.Equal(t, tc.customer, got.CustomerRequired)
		})
	}
}

func TestTemplateInvalidMeasurements(t *testing.T) {
	for _, n := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		_, err := CompareTemplate(nil, []TemplateLine{{SourceLineID: "line", MaterialID: "stone", Quantity: 1, Pieces: []MeasuredPiece{{Name: "Island", LengthMM: n, WidthMM: 100, ThicknessMM: 30}}}})
		require.Error(t, err)
	}
	_, err := CompareTemplate(nil, []TemplateLine{{SourceLineID: "line", MaterialID: "stone", Quantity: 1}})
	require.Error(t, err)
}

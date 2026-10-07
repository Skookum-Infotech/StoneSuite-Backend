package docextract

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docextract/internal/pdftest"
)

func TestParsePDF_ReadsTextInsideFormXObjects(t *testing.T) {
	var p pdftest.Page
	p.Add(700, pdftest.Cell{X: 40, S: "KITCHEN"})
	// Drawn at (0,0) in the XObject, moved into place by the page's cm.
	p.Layers = []pdftest.Layer{{DX: 200, DY: 650, Texts: []pdftest.Text{{X: 0, Y: 0, S: "Install Date"}}}}
	assert.Equal(t, []string{"KITCHEN", "Install Date"}, formRows(t, p))
}

func TestParsePDF_LayerOverPageText(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		layers []pdftest.Layer
		want   string
	}{
		{
			name:   "a label repeated a point off is read once",
			base:   "Sink Model",
			layers: []pdftest.Layer{{DX: 1.5, DY: 0.9, Texts: []pdftest.Text{{X: 40, Y: 700, S: "Sink Model"}}}},
			want:   "Sink Model",
		},
		{
			name: "a later layer's word replaces the one it covers",
			base: "BATH 1",
			layers: []pdftest.Layer{
				{Texts: []pdftest.Text{{X: 40, Y: 700, S: "BATH 1"}}},
				{DX: 0.3, DY: -0.9, Texts: []pdftest.Text{{X: 40, Y: 700, S: "BATH 2"}}},
			},
			want: "BATH 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p pdftest.Page
			p.Add(700, pdftest.Cell{X: 40, S: tt.base})
			p.Layers = tt.layers
			assert.Equal(t, []string{tt.want}, formRows(t, p))
		})
	}
}

func TestCovers(t *testing.T) {
	tests := []struct {
		name string
		a, b Word
		want bool
	}{
		{"same spot", Word{X: 10, W: 6}, Word{X: 10.2, W: 6}, true},
		{"adjacent words", Word{X: 10, W: 6}, Word{X: 17, W: 6}, false},
		{"small overlap", Word{X: 10, W: 6}, Word{X: 15, W: 6}, false},
		{"zero width never covers", Word{X: 10, W: 0}, Word{X: 10, W: 6}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, covers(tt.a, tt.b))
		})
	}
}

func TestMatMul(t *testing.T) {
	scale := mat{2, 0, 0, 2, 0, 0}
	move := translate(10, 20)
	// Scale then move: a point at (1,1) lands at (12,22).
	m := scale.mul(move)
	assert.Equal(t, 12.0, 1*m[0]+1*m[2]+m[4])
	assert.Equal(t, 22.0, 1*m[1]+1*m[3]+m[5])
}

func TestPlaceOver(t *testing.T) {
	a := Word{X: 10, W: 6, Text: "A"}
	b := Word{X: 40, W: 6, Text: "B"}
	c := Word{X: 70, W: 6, Text: "C"}
	t.Run("a duplicate leaves the row untouched even after a covered word", func(t *testing.T) {
		row := []Word{a, b, c}
		got := placeOver(row, Word{X: 71, W: 6, Text: "C"})
		assert.Equal(t, []Word{a, b, c}, got)
		assert.Equal(t, []Word{a, b, c}, row, "the caller's row is never compacted in place")
	})
	t.Run("a covering word replaces the one beneath", func(t *testing.T) {
		got := placeOver([]Word{a, b}, Word{X: 40.5, W: 6, Text: "Z"})
		assert.Equal(t, []Word{a, {X: 40.5, W: 6, Text: "Z"}}, got)
	})
}

func TestParsePDF_XObjectTextIsBudgeted(t *testing.T) {
	// A layer whose text alone exceeds the per-page glyph budget stops there.
	long := make([]pdftest.Text, 0, 300)
	for i := 0; i < 300; i++ {
		long = append(long, pdftest.Text{X: 10, Y: float64(780 - i*2), S: strings.Repeat("x", 1000)})
	}
	var p pdftest.Page
	p.Add(700, pdftest.Cell{X: 40, S: "Title"})
	p.Layers = []pdftest.Layer{{Texts: long}}
	pr, err := ParsePDF(context.Background(), pdftest.Build([]pdftest.Page{p}, pdftest.Options{}), Limits{})
	assert.NoError(t, err)
	glyphs := 0
	for _, r := range pr.Pages[0].Rows {
		for _, w := range r.Words {
			glyphs += len(w.Text)
		}
	}
	assert.LessOrEqual(t, glyphs, xobjMaxGlyphs+len("Title"))
}

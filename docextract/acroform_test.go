package docextract

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract/internal/pdftest"
)

// formRows parses a one-page PDF and returns each row as "label text [form text]".
func formRows(t *testing.T, p pdftest.Page) []string {
	t.Helper()
	pr, err := ParsePDF(context.Background(), pdftest.Build([]pdftest.Page{p}, pdftest.Options{}), Limits{})
	require.NoError(t, err)
	require.Len(t, pr.Pages, 1)
	var out []string
	for _, r := range pr.Pages[0].Rows {
		var parts []string
		for _, w := range r.Words {
			if w.Form {
				parts = append(parts, "["+w.Text+"]")
				continue
			}
			parts = append(parts, w.Text)
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

func labelPage(fields ...pdftest.Field) pdftest.Page {
	var p pdftest.Page
	p.Add(700, pdftest.Cell{X: 40, S: "Color"})
	p.Fields = fields
	return p
}

func box(x0, x1 float64) [4]float64 { return [4]float64{x0, 696, x1, 710} }

func TestParsePDF_FormValues(t *testing.T) {
	pair := [][2]string{{" ", " "}, {"", `4"`}, {"6", `6"`}}
	tests := []struct {
		name  string
		field pdftest.Field
		want  string
	}{
		{"text value joins the label row", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldText, Value: "CARRARO ORO"}, "Color [CARRARO] [ORO]"},
		{"choice value", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldChoice, Value: "POLISHED"}, "Color [POLISHED]"},
		{"blank export shown through its selected index", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldChoice, Value: "", Options: pair, Selected: []int{1}}, `Color [4"]`},
		{"blank export without an index is blank", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldChoice, Value: "", Options: pair}, "Color"},
		{"export value shows its display text", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldChoice, Value: "6", Options: pair}, `Color [6"]`},
		{"whitespace-only choice is blank", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldChoice, Value: " "}, "Color"},
		{"ticked box", pdftest.Field{Rect: box(80, 92), Type: pdftest.FieldCheckbox, Checked: true}, "Color [" + CheckedMark + "]"},
		{"unticked box", pdftest.Field{Rect: box(80, 92), Type: pdftest.FieldCheckbox}, "Color"},
		{"hidden widget", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldText, Value: "SECRET", Hidden: true}, "Color"},
		{"value kept on a parent field", pdftest.Field{Rect: box(80, 200), Type: pdftest.FieldText, Value: "MISTERIO", Inherit: true}, "Color [MISTERIO]"},
		{"ticked box on a parent field", pdftest.Field{Rect: box(80, 92), Type: pdftest.FieldCheckbox, Checked: true, Inherit: true}, "Color [" + CheckedMark + "]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, []string{tt.want}, formRows(t, labelPage(tt.field)))
		})
	}
}

func TestParsePDF_FormValueWithoutALabelRowGetsItsOwnRow(t *testing.T) {
	p := labelPage(pdftest.Field{Rect: [4]float64{40, 640, 300, 654}, Type: pdftest.FieldText, Value: "NO POP-UP OUTLET"})
	assert.Equal(t, []string{"Color", "[NO] [POP-UP] [OUTLET]"}, formRows(t, p))
}

func TestFormWords_StayInsideTheWidget(t *testing.T) {
	ws := formWords(formValue{x0: 100, y0: 0, x1: 140, y1: 14, text: "A VERY LONG NOTE THAT OVERFLOWS"})
	require.NotEmpty(t, ws)
	last := ws[len(ws)-1]
	assert.LessOrEqual(t, last.X+last.W, 140.01, "a long value must not spill into the next column")
	for _, w := range ws {
		assert.True(t, w.Form)
	}
}

func TestPrunePages_KeepsPagesWithTypedValues(t *testing.T) {
	pages := []PageRows{
		{Page: 1, Rows: []Row{{Words: []Word{{Text: "Title"}}}}},
		{Page: 2, Rows: []Row{{Words: []Word{{Text: "Color"}, {Text: "MISTERIO", Form: true}}}}},
	}
	assert.Len(t, PrunePages(pages), 2)
}

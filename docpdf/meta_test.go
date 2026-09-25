package docpdf

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ptPerMM     = 72 / 25.4
	a4HeightMM  = 297.0
	alignDeltaM = 0.02 // fpdf prints positions to 0.01pt
)

// textRun is one string the renderer drew: where it starts, in mm from the
// page's top-left (y is its baseline), and the font size it was drawn at, in pt.
type textRun struct {
	text string
	x, y float64
	size float64
}

// textOpRE matches a font-size change or a positioned string in a page's
// content stream, so the size in force for each string can be tracked in order.
// fpdf names fonts by hash and writes "Tj" with or without a space before it,
// depending on whether the string came from Text or from a cell.
var textOpRE = regexp.MustCompile(`BT /F\S+ ([\d.]+) Tf ET|BT ([\d.]+) ([\d.]+) Td \(((?:[^()\\]|\\.)*)\) ?Tj ET`)

// textRuns lists every string the PDF draws, in drawing order.
func textRuns(out []byte) []textRun {
	var runs []textRun
	size := 0.0
	for _, m := range textOpRE.FindAllStringSubmatch(pdfText(out), -1) {
		if m[1] != "" {
			size, _ = strconv.ParseFloat(m[1], 64)
			continue
		}
		x, _ := strconv.ParseFloat(m[2], 64)
		y, _ := strconv.ParseFloat(m[3], 64)
		runs = append(runs, textRun{text: m[4], x: x / ptPerMM, y: a4HeightMM - y/ptPerMM, size: size})
	}
	return runs
}

// firstRun returns the first drawn string equal to text. The letterhead is
// drawn before everything else on the page, so for the header's own strings
// that is the header's copy.
func firstRun(t *testing.T, runs []textRun, text string) textRun {
	t.Helper()
	for _, r := range runs {
		if r.text == text {
			return r
		}
	}
	require.Failf(t, "not drawn", "no run of %q", text)
	return textRun{}
}

// rightEdge is where a bold run ends, by Helvetica Bold's metrics.
func rightEdge(r textRun) float64 {
	pdf := newDoc()
	pdf.SetFont(fontFace, "B", r.size)
	return r.x + pdf.GetStringWidth(r.text)
}

func renderRuns(t *testing.T, d PrintableDoc) []textRun {
	t.Helper()
	out, err := Render(d)
	require.NoError(t, err)
	return textRuns(out)
}

func TestMetaEdges(t *testing.T) {
	tests := []struct {
		name        string
		hasBox      bool
		left, right float64
	}{
		{"with the amount box the text is inset by its padding", true, metaX + metaPadX, pageW - marginX - metaPadX},
		{"without it the block spans margin to margin", false, metaX, pageW - marginX},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			left, right := metaEdges(tc.hasBox)
			assert.InDelta(t, tc.left, left, 0.0001)
			assert.InDelta(t, tc.right, right, 0.0001)
		})
	}
}

// The issue date, the due date and the amount due read as one column: their
// labels start on one vertical line, their values end on another, and each
// label sits on the same baseline as its value.
func TestRender_MetaBlockLinesUp(t *testing.T) {
	paid := sampleDoc()
	paid.ShowBalance, paid.BalanceDue, paid.GrandTotal = true, 24595.71, 24595.71

	tests := []struct {
		name        string
		doc         PrintableDoc
		amountLabel string
		amount      string
	}{
		{"documents that track payments headline the amount due", paid, "AMOUNT DUE", "$24595.71"},
		{"the rest headline the total", sampleDoc(), "TOTAL", "$811.88"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs := renderRuns(t, tc.doc)
			wantLeft, wantRight := metaEdges(true)

			rows := []struct{ label, value string }{
				{"ISSUE DATE", "2026-08-24"},
				{"DUE DATE", "2026-09-23"},
				{tc.amountLabel, tc.amount},
			}
			for _, row := range rows {
				label, value := firstRun(t, runs, row.label), firstRun(t, runs, row.value)
				assert.InDelta(t, wantLeft, label.x, alignDeltaM, "%s starts on the shared left line", row.label)
				assert.InDelta(t, wantRight, rightEdge(value), alignDeltaM, "%s ends on the shared right line", row.value)
				assert.InDelta(t, value.y, label.y, alignDeltaM, "%s and %s share a baseline", row.label, row.value)
			}
		})
	}
}

func TestRender_MetaBlockWithoutAmountKeepsFullWidth(t *testing.T) {
	d := sampleDoc()
	d.Lines, d.Subtotal, d.TaxTotal, d.GrandTotal = nil, 0, 0, 0
	runs := renderRuns(t, d)

	wantLeft, wantRight := metaEdges(false)
	assert.InDelta(t, wantLeft, firstRun(t, runs, "ISSUE DATE").x, alignDeltaM)
	assert.InDelta(t, wantRight, rightEdge(firstRun(t, runs, "2026-08-24")), alignDeltaM)
	assert.InDelta(t, firstRun(t, runs, "ISSUE DATE").y, firstRun(t, runs, "2026-08-24").y, alignDeltaM)
}

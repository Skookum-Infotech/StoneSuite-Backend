package docpdf

import (
	"testing"

	"github.com/go-pdf/fpdf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linesDoc is sampleDoc with n line items.
func linesDoc(n int) PrintableDoc {
	d := sampleDoc()
	d.Lines = nil
	for i := 0; i < n; i++ {
		d.Lines = append(d.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", Quantity: 1, UnitPrice: 10, LineTotal: 10})
	}
	return d
}

// drawUpToTable draws everything above the summary the way Render does.
func drawUpToTable(d PrintableDoc) *fpdf.Fpdf {
	pdf := newDoc()
	pdf.SetY(drawMasthead(pdf, d) + mastheadGap)
	drawHeader(pdf, d)
	drawParties(pdf, d)
	drawLineTable(pdf, d)
	return pdf
}

func TestDrawSummary_GapBelowTableAtAnyLength(t *testing.T) {
	sawSamePage, sawNewPage := false, false
	for n := 0; n <= 45; n++ {
		d := linesDoc(n)
		pdf := drawUpToTable(d)
		endY, endPage := pdf.GetY(), pdf.PageNo()

		top := drawSummary(pdf, d, 0)

		if pdf.PageNo() == endPage {
			sawSamePage = true
			require.InDelta(t, sectionGap, top-endY, 0.001, "%d lines: gap between the items and the terms", n)
		} else {
			sawNewPage = true
			require.InDelta(t, pageMarginTop, top, 0.001, "%d lines: a summary that doesn't fit starts a fresh page", n)
		}
	}
	assert.True(t, sawSamePage && sawNewPage, "the sweep should cover the summary fitting and not fitting")
}

func TestSectionGap_SameUnderAddressAndUnderItems(t *testing.T) {
	d := sampleDoc()
	pdf := newDoc()
	start := pdf.GetY()
	drawParties(pdf, d)
	partiesEnd := pdf.GetY()

	scratch := newDoc()
	drawAddress(scratch, marginX, start, "BILL TO", d.BillTo)
	underAddress := partiesEnd - scratch.GetY()

	drawLineTable(pdf, d)
	tableEnd := pdf.GetY()
	underItems := drawSummary(pdf, d, 0) - tableEnd

	assert.InDelta(t, sectionGap, underAddress, 0.001)
	assert.InDelta(t, underAddress, underItems, 0.001, "items to terms uses the same gap as address to items")
}

func TestClosingBlock_SummaryAndPaymentShareAPage(t *testing.T) {
	for n := 0; n <= 45; n++ {
		d := linesDoc(n)
		d.ShowPayment, d.Seller.Payment = true, samplePayment
		pdf := drawUpToTable(d)
		endY, endPage := pdf.GetY(), pdf.PageNo()

		top := drawSummary(pdf, d, paymentReserve(pdf, d))
		summaryPage := pdf.PageNo()
		drawPayment(pdf, d)

		require.Equal(t, summaryPage, pdf.PageNo(), "%d lines: payment details must not be left alone on the next page", n)
		require.LessOrEqual(t, pdf.GetY(), pageBottomY+0.001, "%d lines: payment details must end above the footer", n)
		if summaryPage == endPage {
			require.InDelta(t, sectionGap, top-endY, 0.001, "%d lines: gap between the items and the terms", n)
		}
	}
}

func TestSectionTop(t *testing.T) {
	tests := []struct {
		name     string
		startY   float64
		need     float64
		wantTop  float64
		wantPage int
	}{
		{"fits: a gap below the cursor", 100, 50, 100 + sectionGap, 1},
		{"fits exactly at the page bottom", 100, pageBottomY - (100 + sectionGap), 100 + sectionGap, 1},
		{"does not fit: top of a fresh page", 200, 80, pageMarginTop, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pdf := newDoc()
			pdf.SetY(tc.startY)
			assert.InDelta(t, tc.wantTop, sectionTop(pdf, tc.need), 0.001)
			assert.Equal(t, tc.wantPage, pdf.PageNo())
		})
	}
}

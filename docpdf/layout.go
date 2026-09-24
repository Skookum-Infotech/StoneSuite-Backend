package docpdf

import (
	"fmt"

	"github.com/go-pdf/fpdf"
)

const (
	pageMarginTop   = 15.0
	autoBreakMargin = 20.0
	footerTextGap   = 1.5
	footerTextH     = 5.0
	hairlineW       = 0.2

	// footerPageSample is a typical single-digit page label; its width anchors
	// the page count at the right margin (see setFooter).
	footerPageSample = "Page 1 of 1"
)

// newDoc creates an A4 portrait document with the shared margins, page-break
// behaviour and page-count alias, and starts its first page.
func newDoc() *fpdf.Fpdf {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginX, pageMarginTop, marginX)
	pdf.SetCellMargin(0) // all padding is explicit in the layout code
	pdf.SetAutoPageBreak(true, autoBreakMargin)
	pdf.AliasNbPages("{nb}")
	setFooter(pdf, "")
	pdf.AddPage()
	return pdf
}

// setFooter installs the running footer drawn on every page: a hairline, the
// document label at the left and "Page N of M" at the right.
func setFooter(pdf *fpdf.Fpdf, label string) {
	pdf.SetFooterFunc(func() {
		setDraw(pdf, colHairline)
		pdf.SetLineWidth(hairlineW)
		pdf.Line(marginX, pageFooterY, pageW-marginX, pageFooterY)
		pdf.SetFont(fontFace, "", 8)
		setText(pdf, colSubtle)
		pdf.SetXY(marginX, pageFooterY+footerTextGap)
		pdf.CellFormat(contentW/2, footerTextH, label, "", 0, "L", false, 0, "")
		// Left-aligned at a fixed x on purpose: fpdf swaps the {nb} alias for
		// the real page count after layout, so right-aligning the alias would
		// land the text short of the margin by the width difference.
		pdf.SetX(pageW - marginX - pdf.GetStringWidth(footerPageSample))
		pdf.CellFormat(0, footerTextH, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "L", false, 0, "")
		setText(pdf, colInk)
	})
}

// maxF returns the larger of a and b.
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// sectionTop returns the y at which the next section, need mm tall, starts:
// sectionGap below the cursor when it fits on the page, otherwise the top of a
// fresh page (added here). Every break between sections goes through it, so the
// gap under the line table is the same for one item or a hundred.
func sectionTop(pdf *fpdf.Fpdf, need float64) float64 {
	top := pdf.GetY() + sectionGap
	if top+need > pageBottomY {
		pdf.AddPage()
		return pdf.GetY()
	}
	return top
}

// trimNum formats a float without trailing zeros (e.g. 3, 3.5, 8.25).
func trimNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

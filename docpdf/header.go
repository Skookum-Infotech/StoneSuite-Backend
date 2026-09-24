package docpdf

import "github.com/go-pdf/fpdf"

const (
	mastheadGap = 8.0 // below the masthead, before the letterhead

	sellerW     = 100.0
	sellerNameH = 6.5
	sellerLineH = 4.6

	// metaX is the left edge of the meta block. It sits well right of centre so
	// each label stays close to its value instead of floating across the page.
	metaX       = 146.0
	metaRowH    = 7.0
	metaValueH  = 5.5
	metaLabelPt = 7.0
	metaValuePt = 10.0

	// metaPadX is the padding inside the amount box. The date rows are inset by
	// the same amount whenever the box is shown, so the labels of all three rows
	// start on one vertical line and their values end on another, with the box
	// running margin to margin around them (see metaEdges).
	metaPadX     = 3.5
	amountBoxH   = 12.0
	amountBoxGap = 1.5 // between the last meta row and the highlighted box
	amountGap    = 2.0 // least space between the box's label and its amount
	amountRadius = 2.0
	amountMaxPt  = 14.0
	amountMinPt  = 9.0

	// capHeight is Helvetica's capital height as a fraction of the font size.
	capHeight = 0.718

	pillH    = 6.0
	pillPadL = 5.5 // room for the lime dot
	pillPadR = 3.0
	pillDotR = 0.9
)

// metaRow is one label/value line in the top-right meta grid.
type metaRow struct {
	label string
	value string
	pill  bool // render the value as a status pill
}

// amountHeadline is the figure highlighted in the header: the balance still
// due on documents that track payments, otherwise the document total. ok is
// false for documents with no amounts at all (e.g. the vendor contact card).
func amountHeadline(d PrintableDoc) (label string, value float64, ok bool) {
	switch {
	case d.ShowBalance:
		return "AMOUNT DUE", d.BalanceDue, true
	case len(d.Lines) == 0 && d.GrandTotal == 0:
		return "", 0, false
	default:
		return "TOTAL", d.GrandTotal, true
	}
}

// metaRows lists the meta grid rows above the amount box: the dates that have
// a value, plus the status for documents with no amount to headline (they
// would otherwise show nothing about their state). Empty values are omitted
// rather than printed as a bare label.
func metaRows(d PrintableDoc) []metaRow {
	var rows []metaRow
	for _, r := range []metaRow{
		{"ISSUE DATE", d.IssueDate, false},
		{"DUE DATE", d.DueDate, false},
	} {
		if r.value != "" {
			rows = append(rows, r)
		}
	}
	if _, _, ok := amountHeadline(d); !ok && d.Status != "" {
		rows = append(rows, metaRow{"STATUS", d.Status, true})
	}
	return rows
}

// drawHeader draws the letterhead (the seller's name and address) on the left
// and the meta grid (dates and the highlighted amount) on the right, then
// leaves the cursor below whichever is taller.
func drawHeader(pdf *fpdf.Fpdf, d PrintableDoc) {
	top := pdf.GetY()
	sellerEnd := drawSeller(pdf, d.Seller, top)
	metaEnd := drawMeta(pdf, d, top)
	pdf.SetY(maxF(sellerEnd, metaEnd) + sectionGap)
}

// drawSeller writes the seller's name and address block and returns the y
// below it.
func drawSeller(pdf *fpdf.Fpdf, s Seller, top float64) float64 {
	pdf.SetXY(marginX, top)
	pdf.SetFont(fontFace, "B", 14)
	setText(pdf, colInk)
	pdf.MultiCell(sellerW, sellerNameH, s.Name, "", "L", false)

	pdf.SetFont(fontFace, "", 9)
	setText(pdf, colMuted)
	for _, ln := range []string{s.AddrLine1, s.AddrLine2, s.CityStateZip, s.Phone, s.Email} {
		if ln == "" {
			continue
		}
		pdf.SetX(marginX)
		pdf.MultiCell(sellerW, sellerLineH, ln, "", "L", false)
	}
	return pdf.GetY()
}

// metaEdges returns the x where the labels start and the x where the values
// end. With the amount box shown both are pulled in by metaPadX, so the dates
// sit on the same two lines as the text inside the box; without it they span
// the whole block, flush with the margin.
func metaEdges(hasBox bool) (left, right float64) {
	left, right = metaX, pageW-marginX
	if hasBox {
		left += metaPadX
		right -= metaPadX
	}
	return left, right
}

// baselineIn returns the baseline y that centres the capitals of the current
// font in a line box h tall starting at y. A row draws all its text on the one
// baseline its largest text gives, so a small label and a big value share a
// line instead of each floating at its own vertical centre.
func baselineIn(pdf *fpdf.Fpdf, y, h float64) float64 {
	_, size := pdf.GetFontSize()
	return y + h/2 + capHeight*size/2
}

// drawMeta writes the label/value grid at the right, then the highlighted
// amount box under it, and returns the y below the block.
func drawMeta(pdf *fpdf.Fpdf, d PrintableDoc, top float64) float64 {
	label, value, hasBox := amountHeadline(d)
	left, right := metaEdges(hasBox)
	y := top
	for _, r := range metaRows(d) {
		drawMetaRow(pdf, r, left, right, y)
		y += metaRowH
	}
	if hasBox {
		y = drawAmountBox(pdf, y+amountBoxGap, label, money(d.CurrencySymbol, value), left, right)
	}
	return y
}

// drawMetaRow writes one grid row at y: the label starting at left and the
// value ending at right, on one baseline.
func drawMetaRow(pdf *fpdf.Fpdf, r metaRow, left, right, y float64) {
	if r.pill {
		pdf.SetFont(fontFace, "B", metaLabelPt)
		drawMetaLabel(pdf, r.label, left, baselineIn(pdf, y, pillH), colFaint)
		drawStatusPill(pdf, right, y, r.value)
		return
	}
	pdf.SetFont(fontFace, "B", metaValuePt)
	baseline := baselineIn(pdf, y, metaValueH)
	setText(pdf, colInk)
	drawTextRight(pdf, r.value, right, baseline)
	drawMetaLabel(pdf, r.label, left, baseline, colFaint)
}

// drawMetaLabel writes a small bold label starting at x on the given baseline.
func drawMetaLabel(pdf *fpdf.Fpdf, label string, x, baseline float64, c colour) {
	pdf.SetFont(fontFace, "B", metaLabelPt)
	setText(pdf, c)
	pdf.Text(x, baseline, label)
}

// drawTextRight writes s in the current font and colour ending at x on the
// given baseline.
func drawTextRight(pdf *fpdf.Fpdf, s string, x, baseline float64) {
	pdf.Text(x-pdf.GetStringWidth(s), baseline, s)
}

// drawAmountBox draws the lime-highlighted box at y, running from the meta
// block's left edge to the margin. Inside, the label starts at left and the
// amount, shrunk to fit, ends at right, both on one baseline. It returns the y
// below the box.
func drawAmountBox(pdf *fpdf.Fpdf, y float64, label, amount string, left, right float64) float64 {
	setFill(pdf, colLime)
	pdf.RoundedRect(metaX, y, pageW-marginX-metaX, amountBoxH, amountRadius, "1234", "F")

	pdf.SetFont(fontFace, "B", metaLabelPt)
	labelW := pdf.GetStringWidth(label)
	pdf.SetFont(fontFace, "B", fitFontSize(pdf, amount, "B", amountMaxPt, amountMinPt, right-left-labelW-amountGap))
	baseline := baselineIn(pdf, y, amountBoxH)
	setText(pdf, colInk)
	drawTextRight(pdf, amount, right, baseline)
	drawMetaLabel(pdf, label, left, baseline, colText)
	return y + amountBoxH
}

// drawStatusPill draws status in a rounded light chip with a lime dot,
// right-aligned to the given x. All statuses share one neutral style: status
// values are module-specific free text, so colouring by meaning would need a
// map that drifts.
func drawStatusPill(pdf *fpdf.Fpdf, right, y float64, status string) {
	pdf.SetFont(fontFace, "B", 8)
	textW := pdf.GetStringWidth(status)
	w := textW + pillPadL + pillPadR
	x := right - w

	setFill(pdf, colTint)
	setDraw(pdf, colHairline)
	pdf.SetLineWidth(hairlineW)
	pdf.RoundedRect(x, y, w, pillH, pillH/2, "1234", "DF")
	setFill(pdf, colLimeDeep)
	pdf.Circle(x+pillPadL/2+0.5, y+pillH/2, pillDotR, "F")
	setText(pdf, colInk)
	pdf.SetXY(x+pillPadL, y)
	pdf.CellFormat(textW+0.5, pillH, status, "", 0, "L", false, 0, "")
}

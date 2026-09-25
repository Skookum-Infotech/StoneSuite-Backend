package docpdf

import "github.com/go-pdf/fpdf"

const (
	totalsW      = 78.0
	totalsPad    = 4.0
	totalsRowH   = 6.5
	totalsRadius = 2.0
	headlineH    = 11.0
	headlineGap  = 3.0

	notesW     = contentW - totalsW - 10 // left column beside the totals card
	noteLabelH = 4.0
	noteBodyDY = 6.6 // label plus its accent rule, before the body text
	noteLineH  = 4.4
	noteGap    = 4.0
	noteRuleW  = 10.0
	noteRuleH  = 0.5
)

// totalsRow is one label/value line of the totals card.
type totalsRow struct {
	label string
	value float64
	bold  bool
}

// totalsRows returns the rows of the totals card and the headline figure shown
// in the ink block under it: Balance Due when the document tracks payments,
// otherwise Grand Total. Zero adjustments are left out.
func totalsRows(d PrintableDoc) (rows []totalsRow, headline totalsRow) {
	rows = []totalsRow{{"Subtotal", d.Subtotal, false}}
	for _, r := range []totalsRow{
		{"Discount", -d.DiscountTotal, false},
		{"Tax", d.TaxTotal, false},
		{"Shipping", d.ShippingCharge, false},
		{"Adjustment", d.Adjustment, false},
	} {
		if r.value != 0 {
			rows = append(rows, r)
		}
	}
	if d.ShowBalance {
		rows = append(rows, totalsRow{"Grand Total", d.GrandTotal, true}, totalsRow{"Amount Paid", -d.AmountPaid, false})
		return rows, totalsRow{"Balance Due", d.BalanceDue, true}
	}
	return rows, totalsRow{"Grand Total", d.GrandTotal, true}
}

// totalsCardH is the height of the totals card for the given rows.
func totalsCardH(rows []totalsRow) float64 {
	return 2*totalsPad + float64(len(rows))*totalsRowH
}

// noteBlock is a titled free-text block (terms, notes, memo).
type noteBlock struct {
	label string
	body  string
}

// noteBlocks returns the non-empty text blocks in print order.
func noteBlocks(d PrintableDoc) []noteBlock {
	var out []noteBlock
	for _, b := range []noteBlock{
		{"TERMS & CONDITIONS", d.Terms},
		{"NOTES", d.Notes},
		{"MEMO", d.Memo},
	} {
		if b.body != "" {
			out = append(out, b)
		}
	}
	return out
}

// notesHeight is the height the blocks need in the left column, measured in
// the body font they are drawn in.
func notesHeight(pdf *fpdf.Fpdf, blocks []noteBlock) float64 {
	pdf.SetFont(fontFace, "", 9)
	h := 0.0
	for _, b := range blocks {
		h += noteBodyDY + float64(len(pdf.SplitLines([]byte(b.body), notesW)))*noteLineH + noteGap
	}
	return h
}

// drawSummary draws the totals card at the right and the terms/notes/memo
// blocks at the left, side by side, sectionGap below whatever precedes them. If
// they won't fit on the current page they move to a fresh one together instead
// of splitting the card. reserve is height the caller draws straight beneath
// (the payment details): it has to fit on the same page or all of it moves, so
// the two are never split across pages. It returns the y the block starts at.
func drawSummary(pdf *fpdf.Fpdf, d PrintableDoc, reserve float64) float64 {
	rows, headline := totalsRows(d)
	blocks := noteBlocks(d)
	need := maxF(totalsCardH(rows)+headlineGap+headlineH, notesHeight(pdf, blocks))

	top := sectionTop(pdf, need+reserve)
	page := pdf.PageNo()
	drawTotals(pdf, d.CurrencySymbol, rows, headline, top)
	drawNotes(pdf, blocks, top)
	if pdf.PageNo() == page { // absurdly long notes can spill onto a later page
		pdf.SetY(top + need)
	}
	return top
}

// drawTotals draws the light totals card and, under it, the ink headline block.
func drawTotals(pdf *fpdf.Fpdf, sym string, rows []totalsRow, headline totalsRow, top float64) {
	x := pageW - marginX - totalsW
	cardH := totalsCardH(rows)
	setFill(pdf, colTint)
	pdf.RoundedRect(x, top, totalsW, cardH, totalsRadius, "1234", "F")

	labelW := totalsW / 2
	valueW := totalsW/2 - 2*totalsPad
	y := top + totalsPad
	for _, r := range rows {
		style, labelCol, valueCol := "", colMuted, colText
		if r.bold {
			style, labelCol, valueCol = "B", colInk, colInk
		}
		pdf.SetFont(fontFace, style, 9)
		setText(pdf, labelCol)
		pdf.SetXY(x+totalsPad, y)
		pdf.CellFormat(labelW, totalsRowH, r.label, "", 0, "L", false, 0, "")
		setText(pdf, valueCol)
		pdf.CellFormat(valueW, totalsRowH, money(sym, r.value), "", 0, "R", false, 0, "")
		y += totalsRowH
	}

	hy := top + cardH + headlineGap
	setFill(pdf, colInk)
	pdf.RoundedRect(x, hy, totalsW, headlineH, totalsRadius, "1234", "F")
	pdf.SetFont(fontFace, "B", 9)
	setText(pdf, colWhite)
	pdf.SetXY(x+totalsPad, hy)
	pdf.CellFormat(labelW, headlineH, headline.label, "", 0, "L", false, 0, "")
	pdf.SetFont(fontFace, "B", 12)
	setText(pdf, colLime)
	pdf.CellFormat(valueW, headlineH, money(sym, headline.value), "", 0, "R", false, 0, "")
}

// drawNotes stacks the titled text blocks in the left column starting at top.
func drawNotes(pdf *fpdf.Fpdf, blocks []noteBlock, top float64) {
	y := top
	for _, b := range blocks {
		pdf.SetFont(fontFace, "B", 7.5)
		setText(pdf, colSubtle)
		pdf.SetXY(marginX, y)
		pdf.CellFormat(notesW, noteLabelH, b.label, "", 0, "L", false, 0, "")
		setFill(pdf, colLimeDeep)
		pdf.Rect(marginX, y+noteLabelH+0.4, noteRuleW, noteRuleH, "F")

		pdf.SetFont(fontFace, "", 9)
		setText(pdf, colMuted)
		pdf.SetXY(marginX, y+noteBodyDY)
		pdf.MultiCell(notesW, noteLineH, b.body, "", "L", false)
		y = pdf.GetY() + noteGap
	}
}

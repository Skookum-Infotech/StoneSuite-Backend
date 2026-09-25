package docpdf

import "github.com/go-pdf/fpdf"

// Column widths of the line table; they sum to contentW.
const (
	colItemW  = 84.0
	colQtyW   = 15.0
	colPriceW = 26.0
	colDiscW  = 17.0
	colTaxW   = 16.0
	colTotalW = 22.0

	tableHeadH  = 8.0
	tableRadius = 1.5
	rowPadX     = 3.0
	rowPadY     = 2.2
	rowPadR     = 2.5 // right padding inside numeric cells
	nameLineH   = 4.6
	descLineH   = 4.0
)

// tableCol is one column of the line-table header.
type tableCol struct {
	w     float64
	label string
	left  bool // left-aligned (the item column); the rest are numeric
}

// tableCols lists the header columns in order.
func tableCols() []tableCol {
	return []tableCol{
		{colItemW, "ITEM", true},
		{colQtyW, "QTY", false},
		{colPriceW, "PRICE", false},
		{colDiscW, "DISC %", false},
		{colTaxW, "TAX %", false},
		{colTotalW, "TOTAL", false},
	}
}

// drawLineTableHeader draws the ink header row at the cursor.
func drawLineTableHeader(pdf *fpdf.Fpdf) {
	y := pdf.GetY()
	setFill(pdf, colInk)
	pdf.RoundedRect(marginX, y, contentW, tableHeadH, tableRadius, "1234", "F")
	pdf.SetFont(fontFace, "B", 7.5)
	setText(pdf, colWhite)
	x := marginX
	for _, c := range tableCols() {
		if c.left {
			pdf.SetXY(x+rowPadX, y)
			pdf.CellFormat(c.w-rowPadX, tableHeadH, c.label, "", 0, "L", false, 0, "")
		} else {
			pdf.SetXY(x, y)
			pdf.CellFormat(c.w-rowPadR, tableHeadH, c.label, "", 0, "R", false, 0, "")
		}
		x += c.w
	}
	pdf.SetXY(marginX, y+tableHeadH)
}

// lineName is the item cell's first line: "SKU - Name", or just the name.
func lineName(ln PrintLine) string {
	if ln.SKU == "" {
		return ln.Name
	}
	return ln.SKU + " " + emDash + " " + ln.Name
}

// pctCell formats a percentage column value; zero prints as a dash so the
// column reads as "none" rather than as a column of zeros.
func pctCell(v float64) string {
	if v == 0 {
		return enDash
	}
	return trimNum(v)
}

// rowHeight is the height of the line's row: padding plus its wrapped name and
// description lines. Text has to be measured in the font it is drawn in, so it
// sets those fonts as a side effect.
func rowHeight(pdf *fpdf.Fpdf, ln PrintLine) float64 {
	itemW := colItemW - 2*rowPadX
	pdf.SetFont(fontFace, "", 9)
	nameLines := max(1, len(pdf.SplitLines([]byte(lineName(ln)), itemW)))
	h := 2*rowPadY + float64(nameLines)*nameLineH
	if ln.Description != "" {
		pdf.SetFont(fontFace, "", 8)
		h += float64(len(pdf.SplitLines([]byte(ln.Description), itemW))) * descLineH
	}
	return h
}

// drawLineTable draws the header and one row per line, repeating the header on
// each continuation page. Rows are measured before they are drawn so a row
// never straddles a page break and its zebra fill matches its height.
func drawLineTable(pdf *fpdf.Fpdf, d PrintableDoc) {
	drawLineTableHeader(pdf)
	for i, ln := range d.Lines {
		h := rowHeight(pdf, ln)
		if pdf.GetY()+h > pageBottomY {
			pdf.AddPage()
			drawLineTableHeader(pdf)
		}
		drawLineRow(pdf, d.CurrencySymbol, ln, i, h)
	}
}

// drawLineRow draws row idx (zebra-filled when odd) of height h at the cursor
// and leaves the cursor at the row's bottom edge.
func drawLineRow(pdf *fpdf.Fpdf, sym string, ln PrintLine, idx int, h float64) {
	y := pdf.GetY()
	if idx%2 == 1 {
		setFill(pdf, colTint)
		pdf.Rect(marginX, y, contentW, h, "F")
	}

	itemW := colItemW - 2*rowPadX
	pdf.SetFont(fontFace, "", 9)
	setText(pdf, colText)
	pdf.SetXY(marginX+rowPadX, y+rowPadY)
	pdf.MultiCell(itemW, nameLineH, lineName(ln), "", "L", false)
	if ln.Description != "" {
		pdf.SetFont(fontFace, "", 8)
		setText(pdf, colSubtle)
		pdf.SetX(marginX + rowPadX)
		pdf.MultiCell(itemW, descLineH, ln.Description, "", "L", false)
	}

	cells := []struct {
		w    float64
		text string
		bold bool
	}{
		{colQtyW, trimNum(ln.Quantity), false},
		{colPriceW, money(sym, ln.UnitPrice), false},
		{colDiscW, pctCell(ln.DiscountPercent), false},
		{colTaxW, pctCell(ln.TaxPercent), false},
		{colTotalW, money(sym, ln.LineTotal), true},
	}
	x := marginX + colItemW
	for _, c := range cells {
		style, col := "", colText
		if c.bold {
			style, col = "B", colInk
		}
		pdf.SetFont(fontFace, style, 9)
		setText(pdf, col)
		pdf.SetXY(x, y+rowPadY)
		pdf.CellFormat(c.w-rowPadR, nameLineH, c.text, "", 0, "R", false, 0, "")
		x += c.w
	}

	setDraw(pdf, colHairline)
	pdf.SetLineWidth(hairlineW)
	pdf.Line(marginX, y+h, marginX+contentW, y+h)
	pdf.SetXY(marginX, y+h)
}

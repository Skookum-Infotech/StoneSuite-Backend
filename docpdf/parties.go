package docpdf

import "github.com/go-pdf/fpdf"

const (
	partyColW   = 85.0
	partyShipX  = 110.0
	partyLabelH = 4.0
	partyBodyDY = 6.6 // label plus its accent rule, before the first text line
	partyNameH  = 5.0
	partyLineH  = 4.5
	partyRuleW  = 10.0
	partyRuleH  = 0.5
)

// empty reports whether no field of the address is set.
func (a Address) empty() bool { return a == Address{} }

// drawParties draws the Bill To and Ship To blocks side by side. A party with
// no details is omitted instead of leaving a heading over nothing.
func drawParties(pdf *fpdf.Fpdf, d PrintableDoc) {
	top := pdf.GetY()
	end := top
	if !d.BillTo.empty() {
		drawAddress(pdf, marginX, top, "BILL TO", d.BillTo)
		end = maxF(end, pdf.GetY())
	}
	if !d.ShipTo.empty() {
		drawAddress(pdf, partyShipX, top, "SHIP TO", d.ShipTo)
		end = maxF(end, pdf.GetY())
	}
	if end > top {
		end += sectionGap
	}
	pdf.SetY(end)
}

// drawAddress draws one labelled address block at (x, y). Lines wrap inside the
// column (MultiCell) so a long address can't overflow into its neighbour.
func drawAddress(pdf *fpdf.Fpdf, x, y float64, label string, a Address) {
	pdf.SetFont(fontFace, "B", 7.5)
	setText(pdf, colSubtle)
	pdf.SetXY(x, y)
	pdf.CellFormat(partyColW, partyLabelH, label, "", 0, "L", false, 0, "")
	setFill(pdf, colLimeDeep)
	pdf.Rect(x, y+partyLabelH+0.4, partyRuleW, partyRuleH, "F")

	pdf.SetXY(x, y+partyBodyDY)
	if a.Name != "" {
		pdf.SetFont(fontFace, "B", 10)
		setText(pdf, colInk)
		pdf.SetX(x)
		pdf.MultiCell(partyColW, partyNameH, a.Name, "", "L", false)
	}
	pdf.SetFont(fontFace, "", 9)
	setText(pdf, colMuted)
	for _, ln := range []string{a.Attention, a.Line1, a.Line2, a.CityStateZip, a.Phone, a.Email} {
		if ln == "" {
			continue
		}
		pdf.SetX(x)
		pdf.MultiCell(partyColW, partyLineH, ln, "", "L", false)
	}
}

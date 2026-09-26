package docpdf

import (
	"strings"

	"github.com/go-pdf/fpdf"
)

const (
	paymentTitle = "PAYMENT DETAILS"

	paymentPad        = 4.0
	paymentRadius     = 2.0
	paymentTitleH     = 4.0 // heading cell; its accent rule sits just under it
	paymentFieldsDY   = 7.9 // heading plus its accent rule and a gap, before the fields
	paymentColGap     = 6.0
	paymentLabelH     = 3.5
	paymentLabelGap   = 0.6 // between a field's label and its value
	paymentValueLineH = 5.0
	paymentValuePt    = 10.0
)

// paymentField is one label/value pair in the payment details card.
type paymentField struct {
	label string
	value string
}

// paymentFields lists the payment details that have a value, in print order.
// Blank (or whitespace-only) values are left out rather than printed as a bare
// label.
func paymentFields(p PaymentDetails) []paymentField {
	var out []paymentField
	for _, f := range []paymentField{
		{"BANK NAME", p.BankName},
		{"ACCOUNT NUMBER", p.AccountNumber},
		{"WIRE ROUTING NUMBER", p.RoutingNumber},
	} {
		if v := strings.TrimSpace(f.value); v != "" {
			out = append(out, paymentField{f.label, v})
		}
	}
	return out
}

// paymentFieldsFor lists the fields the document prints: none unless it asks
// for the section.
func paymentFieldsFor(d PrintableDoc) []paymentField {
	if !d.ShowPayment {
		return nil
	}
	return paymentFields(d.Seller.Payment)
}

// paymentReserve is the height drawPayment needs beneath the summary (its gap
// and its card), or 0 when it draws nothing. drawSummary keeps that much room
// free so the two never end up on different pages.
func paymentReserve(pdf *fpdf.Fpdf, d PrintableDoc) float64 {
	fields := paymentFieldsFor(d)
	if len(fields) == 0 {
		return 0
	}
	return sectionGap + paymentCardH(pdf, fields)
}

// paymentColW is the width of one field column when n fields share the card.
func paymentColW(n int) float64 {
	inner := contentW - 2*paymentPad
	return (inner - float64(n-1)*paymentColGap) / float64(n)
}

// paymentCardH is the card's height: padding, heading, then one label plus as
// many value lines as the longest wrapped value needs. Values are measured in
// the font they are drawn in, so it sets that font as a side effect.
func paymentCardH(pdf *fpdf.Fpdf, fields []paymentField) float64 {
	pdf.SetFont(fontFace, "B", paymentValuePt)
	colW := paymentColW(len(fields))
	lines := 1
	for _, f := range fields {
		lines = max(lines, len(pdf.SplitLines([]byte(f.value), colW)))
	}
	return 2*paymentPad + paymentFieldsDY + paymentLabelH + paymentLabelGap + float64(lines)*paymentValueLineH
}

// drawPayment draws the Payment Details card sectionGap below the cursor (on a
// fresh page when it doesn't fit) and leaves the cursor at its bottom edge. It
// draws nothing for a document that doesn't ask for it or has no details to
// show.
func drawPayment(pdf *fpdf.Fpdf, d PrintableDoc) {
	fields := paymentFieldsFor(d)
	if len(fields) == 0 {
		return
	}
	h := paymentCardH(pdf, fields)
	top := sectionTop(pdf, h)

	setFill(pdf, colTint)
	pdf.RoundedRect(marginX, top, contentW, h, paymentRadius, "1234", "F")

	x0 := marginX + paymentPad
	y0 := top + paymentPad
	pdf.SetFont(fontFace, "B", 7.5)
	setText(pdf, colSubtle)
	pdf.SetXY(x0, y0)
	pdf.CellFormat(contentW-2*paymentPad, paymentTitleH, paymentTitle, "", 0, "L", false, 0, "")
	setFill(pdf, colLimeDeep)
	pdf.Rect(x0, y0+paymentTitleH+0.4, partyRuleW, partyRuleH, "F")

	colW := paymentColW(len(fields))
	fieldsY := y0 + paymentFieldsDY
	for i, f := range fields {
		x := x0 + float64(i)*(colW+paymentColGap)
		pdf.SetFont(fontFace, "B", 7)
		setText(pdf, colSubtle)
		pdf.SetXY(x, fieldsY)
		pdf.CellFormat(colW, paymentLabelH, f.label, "", 0, "L", false, 0, "")

		pdf.SetFont(fontFace, "B", paymentValuePt)
		setText(pdf, colInk)
		pdf.SetXY(x, fieldsY+paymentLabelH+paymentLabelGap)
		pdf.MultiCell(colW, paymentValueLineH, f.value, "", "L", false)
	}
	pdf.SetY(top + h)
}

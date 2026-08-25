package docpdf

import (
	"fmt"

	"github.com/go-pdf/fpdf"
)

const (
	marginX     = 15.0
	pageBottomY = 270.0 // A4 height 297mm minus footer zone
	fontFace    = "Helvetica"
)

func newDoc() *fpdf.Fpdf {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginX, 15, marginX)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()
	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont(fontFace, "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})
	pdf.AliasNbPages("{nb}")
	return pdf
}

func drawHeader(pdf *fpdf.Fpdf, d PrintableDoc) {
	startY := pdf.GetY()
	titleRowH := 8.0

	pdf.SetFont(fontFace, "B", 16)
	pdf.Cell(100, titleRowH, d.Seller.Name)
	pdf.SetFont(fontFace, "B", 20)
	pdf.CellFormat(0, titleRowH, d.Kind, "", 1, "R", false, 0, "")

	pdf.SetFont(fontFace, "", 9)
	for _, ln := range []string{d.Seller.AddrLine1, d.Seller.AddrLine2, d.Seller.CityStateZip, d.Seller.Phone, d.Seller.Email} {
		if ln == "" {
			continue
		}
		pdf.CellFormat(100, 4.5, ln, "", 1, "L", false, 0, "")
	}
	sellerEndY := pdf.GetY()

	pdf.SetY(startY + titleRowH)
	pdf.SetFont(fontFace, "", 10)
	pdf.CellFormat(0, 5, "No: "+d.Number, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, "Status: "+d.Status, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, "Date: "+d.IssueDate, "", 1, "R", false, 0, "")
	if d.DueDate != "" {
		pdf.CellFormat(0, 5, "Due: "+d.DueDate, "", 1, "R", false, 0, "")
	}
	metaEndY := pdf.GetY()

	pdf.SetY(maxF(sellerEndY, metaEndY))
	pdf.Ln(4)
}

func drawParties(pdf *fpdf.Fpdf, d PrintableDoc) {
	top := pdf.GetY()
	drawAddress(pdf, marginX, top, "BILL TO", d.BillTo)
	billToEndY := pdf.GetY()
	drawAddress(pdf, 110, top, "SHIP TO", d.ShipTo)
	shipToEndY := pdf.GetY()
	pdf.SetY(maxF(billToEndY, shipToEndY) + 4)
}

// maxF returns the larger of a and b.
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func drawAddress(pdf *fpdf.Fpdf, x, y float64, label string, a Address) {
	pdf.SetXY(x, y)
	pdf.SetFont(fontFace, "B", 9)
	pdf.CellFormat(85, 5, label, "", 2, "L", false, 0, "")
	pdf.SetFont(fontFace, "", 9)
	for _, ln := range []string{a.Name, a.Attention, a.Line1, a.Line2, a.CityStateZip, a.Phone, a.Email} {
		if ln == "" {
			continue
		}
		pdf.SetX(x)
		pdf.CellFormat(85, 4.5, ln, "", 2, "L", false, 0, "")
	}
}

// column widths for the line table (sum ≈ 180mm printable width)
var lineCols = struct{ item, qty, price, disc, tax, total float64 }{90, 15, 25, 15, 15, 20}

func drawLineTableHeader(pdf *fpdf.Fpdf) {
	pdf.SetFont(fontFace, "B", 9)
	pdf.SetFillColor(235, 235, 235)
	pdf.CellFormat(lineCols.item, 7, "Item", "1", 0, "L", true, 0, "")
	pdf.CellFormat(lineCols.qty, 7, "Qty", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.price, 7, "Price", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.disc, 7, "Disc%", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.tax, 7, "Tax%", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.total, 7, "Total", "1", 1, "R", true, 0, "")
}

func drawLineTable(pdf *fpdf.Fpdf, d PrintableDoc) {
	drawLineTableHeader(pdf)
	pdf.SetFont(fontFace, "", 9)
	for _, ln := range d.Lines {
		if pdf.GetY() > pageBottomY {
			pdf.AddPage()
			drawLineTableHeader(pdf)
			pdf.SetFont(fontFace, "", 9)
		}
		name := ln.Name
		if ln.SKU != "" {
			name = ln.SKU + " — " + name
		}
		if ln.Description != "" {
			name = name + "\n" + ln.Description
		}
		x, y := pdf.GetX(), pdf.GetY()
		pdf.MultiCell(lineCols.item, 5, name, "1", "L", false)
		rowH := pdf.GetY() - y
		pdf.SetXY(x+lineCols.item, y)
		pdf.CellFormat(lineCols.qty, rowH, trimNum(ln.Quantity), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.price, rowH, money(d.CurrencySymbol, ln.UnitPrice), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.disc, rowH, trimNum(ln.DiscountPercent), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.tax, rowH, trimNum(ln.TaxPercent), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.total, rowH, money(d.CurrencySymbol, ln.LineTotal), "1", 1, "R", false, 0, "")
	}
	pdf.Ln(3)
}

func drawTotals(pdf *fpdf.Fpdf, d PrintableDoc) {
	type row struct {
		label string
		val   float64
		show  bool
		bold  bool
	}
	rows := []row{
		{"Subtotal", d.Subtotal, true, false},
		{"Discount", -d.DiscountTotal, d.DiscountTotal != 0, false},
		{"Tax", d.TaxTotal, d.TaxTotal != 0, false},
		{"Shipping", d.ShippingCharge, d.ShippingCharge != 0, false},
		{"Adjustment", d.Adjustment, d.Adjustment != 0, false},
		{"Grand Total", d.GrandTotal, true, true},
		{"Amount Paid", -d.AmountPaid, d.ShowBalance, false},
		{"Balance Due", d.BalanceDue, d.ShowBalance, true},
	}
	labelW, valW := 40.0, 30.0
	x := 210 - marginX - labelW - valW
	for _, r := range rows {
		if !r.show {
			continue
		}
		style := ""
		if r.bold {
			style = "B"
		}
		pdf.SetX(x)
		pdf.SetFont(fontFace, style, 10)
		pdf.CellFormat(labelW, 6, r.label, "", 0, "R", false, 0, "")
		pdf.CellFormat(valW, 6, money(d.CurrencySymbol, r.val), "", 1, "R", false, 0, "")
	}
}

func drawFooter(pdf *fpdf.Fpdf, d PrintableDoc) {
	pdf.Ln(6)
	block := func(label, body string) {
		if body == "" {
			return
		}
		pdf.SetFont(fontFace, "B", 9)
		pdf.CellFormat(0, 5, label, "", 1, "L", false, 0, "")
		pdf.SetFont(fontFace, "", 9)
		pdf.MultiCell(0, 4.5, body, "", "L", false)
		pdf.Ln(2)
	}
	block("Terms & Conditions", d.Terms)
	block("Notes", d.Notes)
	block("Memo", d.Memo)
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

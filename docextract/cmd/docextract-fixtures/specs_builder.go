package main

import "stonesuite-backend/docextract/internal/pdftest"

// Builder purchase-order layout (a homebuilder's "BRIX"-style PO): the buyer is
// the letterhead (no Bill To label), the PO number sits in a boxed title two
// rows below "Purchase Order", a Date/Job/Plan/Cost Code strip precedes the
// line table, the table has an "Extension" column that repeats the quantity,
// descriptions wrap over several rows, and page two carries only totals, an
// options table and footers. Names, addresses and numbers are made up; the
// geometry mirrors the real documents.

const (
	bQtyR    = 96.0  // quantity column right edge
	bItemX   = 105.0 // item # column start
	bDescX   = 185.0 // description column start
	bExtR    = 375.0 // extension column right edge
	bCostR   = 446.0 // item cost column right edge
	bUMX     = 455.0 // U/M column start
	bAmountR = 552.0 // ext. cost column right edge
	bLeftX   = 54.0
	bBoxX    = 478.0
	bShipX   = 324.0
	bWrap    = 10.8
	bRow     = 11.7
)

type builderLine struct {
	qty, item, desc, ext, cost, um, amount string
	wraps                                  []string
}

func builderPOSpec() docSpec {
	exp := expectedDoc{
		Header: hdr("00412-K5521", "2026-05-06", "", "", "Lakeview Homes", map[string]string{
			"subtotal": "3557.00", "tax": "15.51", "total": "3572.51",
			"bill_to": "Lakeview Homes\n4410 Commerce Parkway, Suite 200\nFrisco, TX 75034",
			"ship_to": "118 Juniper Court\nCelina, TX 75009\nPrairie Ridge 50s\nLot 12 Blk C Sect 3",
		}),
		Lines: []expectedLine{
			{Description: "Countertops Labor/Turnkey (P/O)", Qty: "1", UnitPrice: "3212.51", Amount: "3212.51"},
			{Description: "Countertops Labor/Turnkey (P/O) 0600 - KITCHEN CTOPS - PERIMETER AND ISLAND", Qty: "83", UnitPrice: "25.00", Amount: "2075.00"},
			{Description: "Countertops Labor/Turnkey (P/O) 0605 - KITCHEN 4\" SPLASH", Qty: "9", UnitPrice: "-25.00", Amount: "-225.00"},
			{Description: "Countertops Labor/Turnkey (P/O) 0578 - DINING HUTCH OPTION COUNTERTOPS Option: 1450-32", Qty: "11", UnitPrice: "25.00", Amount: "275.00"},
			{SKU: "93310E", Description: "Kitchen Sink - Stainless Steel Single Undermount - Installed", Qty: "1", UoM: "EA", UnitPrice: "188.00", Amount: "188.00"},
			{SKU: "990017", Description: "Negotiated Price Adjustment", Qty: "1", UoM: "EA", UnitPrice: "-1968.51", Amount: "-1968.51"},
		},
	}
	return docSpec{name: "12_builder_po_letterhead", exp: exp, raw: builderPOPages}
}

func builderPOPages() []pdftest.Page {
	var p1 pdftest.Page
	p1.Add(718, pdftest.Cell{X: bLeftX, S: "Lakeview Homes"}, pdftest.Cell{X: 468, S: "Purchase Order"})
	p1.Add(706.5, pdftest.Cell{X: bLeftX, S: "4410 Commerce Parkway, Suite 200"})
	p1.Add(698.2, pdftest.Cell{X: bBoxX, S: "00412-K5521"})
	p1.Add(695.7, pdftest.Cell{X: bLeftX, S: "Frisco, TX 75034"})
	p1.Add(643, pdftest.Cell{X: bLeftX, S: "Vendor Address"}, pdftest.Cell{X: bShipX, S: "Delivery Address"})
	p1.Add(612.3, pdftest.Cell{X: bLeftX, S: "Granite Works, LLC."}, pdftest.Cell{X: 259, S: "21563"}, pdftest.Cell{X: bShipX, S: "118 Juniper Court"})
	p1.Add(601.5, pdftest.Cell{X: bLeftX, S: "900 Quarry Rd, Suite 10"}, pdftest.Cell{X: bShipX, S: "Celina, TX 75009"})
	p1.Add(590.6, pdftest.Cell{X: bLeftX, S: "Irving, TX 75038"}, pdftest.Cell{X: bShipX, S: "Prairie Ridge 50s"})
	p1.Add(579.8, pdftest.Cell{X: bLeftX, S: "Phone: (555) 010-2000"}, pdftest.Cell{X: bShipX, S: "Lot 12 Blk C Sect 3"})
	builderStrip(&p1, 542)
	p1.Add(523.2, pdftest.Cell{X: 79, S: "5/6/2026"}, pdftest.Cell{X: 161, S: "00412-118"}, pdftest.Cell{X: 245, S: "R-Cont-RM"},
		pdftest.Cell{X: 339, S: "3811"}, pdftest.Cell{X: 415, S: "Countertops Labor/Turnkey (P/O)"})
	builderTableHeader(&p1, 489)
	y := 472.0
	y = builderRow(&p1, y, builderLine{qty: "1.00", desc: "Countertops Labor/Turnkey", ext: "1.0000", cost: "3,212.5100", amount: "3,212.51", wraps: []string{"(P/O)"}})
	// A change-order group heading after a blank line: a note, not a line.
	y -= bWrap
	p1.Add(y, pdftest.Cell{X: bDescX, S: "CO06"})
	y -= bWrap
	p1.Add(y, pdftest.Cell{X: bDescX, S: "Option: 3900-CO"})
	y -= bRow
	y = builderRow(&p1, y, builderLine{qty: "83.00", desc: "Countertops Labor/Turnkey", ext: "83.0000", cost: "25.0000", amount: "2,075.00", wraps: []string{"(P/O)", "0600 - KITCHEN CTOPS -", "PERIMETER AND ISLAND"}})
	y = builderRow(&p1, y, builderLine{qty: "9.00", desc: "Countertops Labor/Turnkey", ext: "9.0000", cost: "-25.0000", amount: "-225.00", wraps: []string{"(P/O)", "0605 - KITCHEN 4\" SPLASH"}})
	y = builderRow(&p1, y, builderLine{qty: "11.00", desc: "Countertops Labor/Turnkey", ext: "11.0000", cost: "25.0000", amount: "275.00", wraps: []string{"(P/O)", "0578 - DINING HUTCH OPTION", "COUNTERTOPS", "Option: 1450-32"}})
	y = builderRow(&p1, y, builderLine{qty: "1.00", item: "93310E", desc: "Kitchen Sink - Stainless Steel", ext: "1.0000", cost: "188.0000", um: "EA", amount: "188.00", wraps: []string{"Single Undermount - Installed"}})
	builderRow(&p1, y, builderLine{qty: "1.00", item: "990017", desc: "Negotiated Price Adjustment", ext: "1.0000", cost: "-1,968.5100", um: "EA", amount: "-1,968.51"})
	builderFooter(&p1, "Page 1 of 2")

	var p2 pdftest.Page
	p2.Add(717, pdftest.Cell{X: 476, S: "...continued"})
	p2.Add(698.2, pdftest.Cell{X: bBoxX, S: "00412-K5521"})
	builderStrip(&p2, 639)
	builderTableHeader(&p2, 586)
	p2.Add(569, pdftest.Cell{X: 384, S: "SubTotal"}, pdftest.Right(bAmountR, "3,557.00"))
	p2.Add(556.2, pdftest.Cell{X: 384, S: "Sales Tax 8.2500 %"}, pdftest.Right(bAmountR, "15.51"))
	p2.Add(543.3, pdftest.Cell{X: 384, S: "Total"}, pdftest.Right(bAmountR, "3,572.51"))
	p2.Add(113.7, pdftest.Cell{X: 57, S: "Quantity"}, pdftest.Cell{X: 111, S: "Option"})
	p2.Add(96.7, pdftest.Right(bQtyR, "1.00"), pdftest.Cell{X: 111, S: "1450-32"}, pdftest.Cell{X: 181, S: "Cabinet Options, Hutch"})
	p2.Add(85, pdftest.Right(bQtyR, "0.00"), pdftest.Cell{X: 111, S: "3900-CO"}, pdftest.Cell{X: 181, S: "Unique Special, Change Order Buyer Options"})
	builderFooter(&p2, "Page 2 of 2")
	return []pdftest.Page{p1, p2}
}

func builderStrip(p *pdftest.Page, y float64) {
	p.Add(y, pdftest.Cell{X: 86, S: "Date"}, pdftest.Cell{X: 153, S: "Job Number"}, pdftest.Cell{X: 254, S: "Plan"},
		pdftest.Cell{X: 326, S: "Cost Code"}, pdftest.Cell{X: 425, S: "Cost Code Description"})
}

func builderTableHeader(p *pdftest.Page, y float64) {
	p.Add(y, pdftest.Cell{X: 57, S: "Quantity"}, pdftest.Cell{X: bItemX, S: "Item #"}, pdftest.Cell{X: bDescX, S: "Description"},
		pdftest.Right(bExtR, "Extension"), pdftest.Right(bCostR, "Item Cost"), pdftest.Cell{X: bUMX, S: "U/M"}, pdftest.Right(bAmountR, "Ext. Cost"))
}

// builderRow draws one line and its wrapped description rows; it returns the
// y of the next line.
func builderRow(p *pdftest.Page, y float64, l builderLine) float64 {
	p.Add(y, nonEmpty([]pdftest.Cell{
		pdftest.Right(bQtyR, l.qty), {X: bItemX, S: l.item}, {X: bDescX, S: l.desc},
		pdftest.Right(bExtR, l.ext), pdftest.Right(bCostR, l.cost), {X: bUMX, S: l.um}, pdftest.Right(bAmountR, l.amount),
	})...)
	for _, w := range l.wraps {
		y -= bWrap
		p.Add(y, pdftest.Cell{X: bDescX, S: w})
	}
	return y - bRow
}

func builderFooter(p *pdftest.Page, pageLabel string) {
	p.Add(67.7, pdftest.Cell{X: bLeftX, S: "Original"})
	p.Add(56.7, pdftest.Cell{X: bLeftX, S: pageLabel})
	p.Add(36, pdftest.Cell{X: 57, S: "Seller's prices of taxable items include all Texas State and local sales and use taxes unless Texas form 01-339 or 01-919 has been provided."})
}

// builderExtraPOSpec is the single-line "EXTRA" variant: a letter-spaced
// E X T R A sits between the title and a PO number 31 points down, and the
// buyer typed a remark plus contact boilerplate into the description column.
func builderExtraPOSpec() docSpec {
	exp := expectedDoc{
		Header: hdr("08005-81726", "2026-10-01", "", "", "Lakeview Homes", map[string]string{
			"total":   "500.00",
			"ship_to": "77 Larkspur Lane\nDenton, TX 76207\nMeadow Glen 50s\nLot 21 Blk E Sect 1B",
		}),
		Lines: []expectedLine{
			{Description: "Countertops Labor/Turnkey (P/O) counter top upgrades per co 15.", Qty: "1", UnitPrice: "500.00", Amount: "500.00"},
		},
	}
	return docSpec{name: "13_builder_po_extra", exp: exp, raw: builderExtraPOPages}
}

func builderExtraPOPages() []pdftest.Page {
	var p pdftest.Page
	p.Add(718, pdftest.Cell{X: bLeftX, S: "Lakeview Homes"}, pdftest.Cell{X: 468, S: "Purchase Order"})
	p.Add(706.5, pdftest.Cell{X: bLeftX, S: "4410 Commerce Parkway, Suite 200"})
	p.Add(702, pdftest.Cell{X: 477, S: "E"}, pdftest.Cell{X: 488, S: "X"}, pdftest.Cell{X: 499, S: "T"}, pdftest.Cell{X: 510, S: "R"}, pdftest.Cell{X: 522, S: "A"})
	p.Add(695.7, pdftest.Cell{X: bLeftX, S: "Frisco, TX 75034"})
	p.Add(687.2, pdftest.Cell{X: 479, S: "08005-81726"})
	p.Add(643, pdftest.Cell{X: bLeftX, S: "Vendor Address"}, pdftest.Cell{X: bShipX, S: "Delivery Address"})
	p.Add(612.3, pdftest.Cell{X: bLeftX, S: "Granite Works, LLC."}, pdftest.Cell{X: bShipX, S: "77 Larkspur Lane"})
	p.Add(601.5, pdftest.Cell{X: bLeftX, S: "900 Quarry Rd, Suite 10"}, pdftest.Cell{X: bShipX, S: "Denton, TX 76207"})
	p.Add(590.6, pdftest.Cell{X: bLeftX, S: "Irving, TX 75038"}, pdftest.Cell{X: bShipX, S: "Meadow Glen 50s"})
	p.Add(579.8, pdftest.Cell{X: bLeftX, S: "Phone: (555) 010-2000"}, pdftest.Cell{X: bShipX, S: "Lot 21 Blk E Sect 1B"})
	builderStrip(&p, 542)
	p.Add(523.2, pdftest.Cell{X: 77, S: "10/1/2026"}, pdftest.Cell{X: 161, S: "08005-039"}, pdftest.Cell{X: 245, S: "L-LAYT-CM"},
		pdftest.Cell{X: 339, S: "3811"}, pdftest.Cell{X: 415, S: "Countertops Labor/Turnkey (P/O)"})
	builderTableHeader(&p, 489)
	y := builderRow(&p, 472, builderLine{qty: "1.00", desc: "Countertops Labor/Turnkey", ext: "1.0000", cost: "500.0000", amount: "500.00", wraps: []string{
		"(P/O)", "counter top upgrades per co", "15.", "If You Have Questions /", "Problems", "With This Order Contact:",
		"Sam Example", "sam.example@example.com", "Construction",
	}})
	p.Add(y, pdftest.Cell{X: 384, S: "Total"}, pdftest.Right(bAmountR, "500.00"))
	p.Add(68, pdftest.Cell{X: bLeftX, S: "Original"}, pdftest.Cell{X: 266, S: "Variance Code - C"})
	p.Add(57, pdftest.Cell{X: bLeftX, S: "Page 1 of 1"}, pdftest.Cell{X: 260, S: "Custom/Option Budget"})
	p.Add(36, pdftest.Cell{X: 57, S: "Seller's prices of taxable items include all Texas State and local sales and use taxes unless Texas form 01-339 or 01-919 has been provided."})
	return []pdftest.Page{p}
}

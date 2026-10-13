package main

import "stonesuite-backend/docextract/internal/pdftest"

// Purchase-order fixtures. "Elevation Stone" is the tenant's own company in
// every one of them; the other names, addresses and numbers are made up.

const kindPurchaseOrder = "purchase_order"

var (
	ownBlock   = []string{"Elevation Stone", "2200 Foundry Road", "Austin, TX 78702"}
	ownYard    = []string{"Elevation Stone - Yard", "2200 Foundry Road", "Austin, TX 78702"}
	letterhead = []string{"12 Quarry Lane", "Marble Falls, TX 78654"}
)

// vendorHdr is the expected header of a vendor-side document.
func vendorHdr(vendor string, extra map[string]string) map[string]string {
	h := map[string]string{"vendor_name": vendor, "currency": "USD"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// poTable draws the standard table header, rows and totals starting at y.
func poTable(p *pdftest.Page, y float64, lines []lineSpec, totals []kv) float64 {
	p.Add(y, tableHeader()...)
	y = addRows(p, y-rowGap, lines) - rowGap
	for _, t := range totals {
		p.Add(y, pdftest.Cell{X: xLabel, S: t.Label}, pdftest.Right(xAmount, t.Amount))
		y -= rowGap
	}
	return y
}

func purchaseOrderSpecs() []docSpec {
	return []docSpec{vendorQuoteSpec(), ownPOSpec(), builderFormAsPOSpec(), fillablePOSpec()}
}

// vendorQuoteSpec: the vendor's quote. The vendor is the letterhead, we are
// the "Quote To" party, and the quote number is the document's number.
func vendorQuoteSpec() docSpec {
	lines := []lineSpec{
		{"QZ-310", "Quartz slab 30mm Arctic White", "6", "EA", "$480.00", "$2,880.00", "", false},
		{"GR-212", "Granite slab Nero Impala", "2", "EA", "$615.00", "$1,230.00", "", false},
	}
	exp := expectedDoc{
		Header: vendorHdr("Quarry Direct Supply", map[string]string{
			"po_number": "Q-30417", "order_date": "2026-03-10", "payment_terms": "Net 30",
			"subtotal": "4110.00", "total": "4110.00",
		}),
		Lines: expLines(lines...), Class: "quote",
	}
	return docSpec{kind: kindPurchaseOrder, name: "01_vendor_quote", exp: exp, raw: func() []pdftest.Page {
		var p pdftest.Page
		p.Add(750, pdftest.Cell{X: xItem, S: "Quarry Direct Supply"})
		p.Add(738, pdftest.Cell{X: xItem, S: letterhead[0]})
		p.Add(726, pdftest.Cell{X: xItem, S: letterhead[1]})
		p.Add(690, pdftest.Cell{X: xItem, S: "QUOTATION"})
		p.Add(670, pdftest.Cell{X: xItem, S: "Quote #: Q-30417"}, pdftest.Cell{X: xRight, S: "Date: 03/10/2026"})
		p.Add(654, pdftest.Cell{X: xRight, S: "Terms: Net 30"})
		p.Add(630, pdftest.Cell{X: xItem, S: "Quote To:"}, pdftest.Cell{X: xRight, S: "Ship To:"})
		for i := range ownBlock {
			p.Add(614-float64(i)*rowGap, pdftest.Cell{X: xItem, S: ownBlock[i]}, pdftest.Cell{X: xRight, S: ownYard[i]})
		}
		poTable(&p, yTable-40, lines, []kv{{"Subtotal", "$4,110.00"}, {"Total", "$4,110.00"}})
		return []pdftest.Page{p}
	}}
}

// ownPOSpec: our own purchase order from another system. Our name is the
// letterhead; the vendor sits in a "Vendor:" block.
func ownPOSpec() docSpec {
	lines := []lineSpec{
		{"GR-500", "Granite slab Absolute Black", "4", "EA", "$700.00", "$2,800.00", "", false},
		{"TL-90", "Tile adhesive 25kg bag", "10", "EA", "$18.00", "$180.00", "", false},
	}
	exp := expectedDoc{
		Header: vendorHdr("Granite Works LLC", map[string]string{
			"po_number": "EP-5521", "order_date": "2026-04-02", "delivery_date": "2026-04-20", "payment_terms": "Net 30",
			"subtotal": "2980.00", "total": "2980.00",
		}),
		Lines: expLines(lines...), Class: "purchase_order",
	}
	return docSpec{kind: kindPurchaseOrder, name: "02_own_po_vendor_block", exp: exp, raw: func() []pdftest.Page {
		var p pdftest.Page
		p.Add(750, pdftest.Cell{X: xItem, S: ownBlock[0]})
		p.Add(738, pdftest.Cell{X: xItem, S: ownBlock[1]})
		p.Add(726, pdftest.Cell{X: xItem, S: ownBlock[2]})
		p.Add(690, pdftest.Cell{X: xItem, S: "PURCHASE ORDER"})
		p.Add(670, pdftest.Cell{X: xItem, S: "PO Number: EP-5521"}, pdftest.Cell{X: xRight, S: "Order Date: 04/02/2026"})
		p.Add(654, pdftest.Cell{X: xItem, S: "Terms: Net 30"}, pdftest.Cell{X: xRight, S: "Delivery Date: 04/20/2026"})
		p.Add(630, pdftest.Cell{X: xItem, S: "Vendor:"}, pdftest.Cell{X: xRight, S: "Ship To:"})
		vendor := []string{"Granite Works LLC", "900 Quarry Rd, Suite 10", "Tulsa, OK 74103"}
		for i := range vendor {
			p.Add(614-float64(i)*rowGap, pdftest.Cell{X: xItem, S: vendor[i]}, pdftest.Cell{X: xRight, S: ownYard[i]})
		}
		poTable(&p, yTable-40, lines, []kv{{"Subtotal", "$2,980.00"}, {"Total", "$2,980.00"}})
		return []pdftest.Page{p}
	}}
}

// builderFormAsPOSpec: the builder's countertop order form uploaded as a PO.
// Same lines and details as the sales-order reading, but the builder is our
// customer, so neither a vendor nor a customer is named.
func builderFormAsPOSpec() docSpec {
	exp := expectedDoc{
		Header: map[string]string{
			"delivery_date": "2026-11-13", "ship_to": "418 Willow Bend\nCelina 75009",
			"notes": "Special instructions: NO POP-UP OUTLET\n" +
				"Builder contact: Dana Reyes, 214-555-0142, dana.reyes@lakeviewhomes.com\n" +
				"Community: Brookfield. Plan: Aspen-C. Measure date: 11/3/26.",
		},
		Lines: []expectedLine{
			{Description: "CALACATTA LUX", Qty: "1"},
			{Description: "CALACATTA LUX", Qty: "1"},
			{SKU: "440185", Description: "440185 DOUBLE WHITE", Qty: "1"},
		},
		Class: "order_form", Warnings: []string{"no_prices"},
	}
	return docSpec{kind: kindPurchaseOrder, name: "03_builder_order_form", exp: exp, raw: orderFormPages}
}

// fillablePOSpec: a fillable (AcroForm) PO whose vendor, number and date are
// typed into widgets, so the page text holds only the printed labels.
func fillablePOSpec() docSpec {
	lines := []lineSpec{
		{"MB-77", "Carrara marble vanity top", "3", "EA", "$310.00", "$930.00", "", false},
	}
	exp := expectedDoc{
		Header: vendorHdr("Summit Slab Co", map[string]string{
			"po_number": "FP-7001", "order_date": "2026-05-06", "subtotal": "930.00", "total": "930.00",
		}),
		Lines: expLines(lines...), Class: "purchase_order",
	}
	return docSpec{kind: kindPurchaseOrder, name: "04_fillable_po", exp: exp, raw: func() []pdftest.Page {
		var p pdftest.Page
		p.Add(750, pdftest.Cell{X: xItem, S: ownBlock[0]})
		p.Add(738, pdftest.Cell{X: xItem, S: ownBlock[1]})
		p.Add(726, pdftest.Cell{X: xItem, S: ownBlock[2]})
		p.Add(690, pdftest.Cell{X: xItem, S: "PURCHASE ORDER"})
		p.Add(670, pdftest.Cell{X: xItem, S: "PO Number:"}, pdftest.Cell{X: xRight, S: "Order Date:"})
		p.Add(646, pdftest.Cell{X: xItem, S: "Vendor:"})
		poTable(&p, yTable-40, lines, []kv{{"Subtotal", "$930.00"}, {"Total", "$930.00"}})
		p.Fields = []pdftest.Field{
			field(pdftest.FieldText, "FP-7001", 110, 666, 250, 680),
			field(pdftest.FieldText, "05/06/2026", 400, 666, 520, 680),
			field(pdftest.FieldText, "Summit Slab Co", 90, 642, 250, 656),
		}
		return []pdftest.Page{p}
	}}
}

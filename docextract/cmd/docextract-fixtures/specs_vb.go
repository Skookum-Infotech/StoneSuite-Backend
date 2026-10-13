package main

import "stonesuite-backend/docextract/internal/pdftest"

// Vendor-bill fixtures. Names, addresses and numbers are made up; the tenant's
// own company is "Elevation Stone".

const kindVendorBill = "vendor_bill"

func vendorBillSpecs() []docSpec {
	return []docSpec{billPORefSpec(), billMultiPageSpec(), statementSpec()}
}

// billPORefSpec: a plain bill with our PO number and Net 30 terms but no due
// date, so the due date is computed from the terms.
func billPORefSpec() docSpec {
	lines := []lineSpec{
		{"GR-500", "Granite slab Absolute Black", "4", "EA", "$700.00", "$2,800.00", "", false},
		{"TL-90", "Tile adhesive 25kg bag", "10", "EA", "$18.00", "$180.00", "", false},
	}
	exp := expectedDoc{
		Header: vendorHdr("Granite Works LLC", map[string]string{
			"invoice_number": "GW-20418", "invoice_date": "2026-04-15", "due_date": "2026-05-15", "po_number": "EP-5521",
			"payment_terms": "Net 30", "subtotal": "2980.00", "tax": "245.85", "total": "3225.85",
		}),
		Lines: expLines(lines...), Class: "invoice", Warnings: []string{"due_date_from_terms"},
	}
	return docSpec{kind: kindVendorBill, name: "01_bill_po_ref_net30", exp: exp, raw: func() []pdftest.Page {
		var p pdftest.Page
		p.Add(750, pdftest.Cell{X: xItem, S: "Granite Works LLC"})
		p.Add(738, pdftest.Cell{X: xItem, S: "900 Quarry Rd, Suite 10"})
		p.Add(726, pdftest.Cell{X: xItem, S: "Tulsa, OK 74103"})
		p.Add(690, pdftest.Cell{X: xItem, S: "INVOICE"})
		p.Add(670, pdftest.Cell{X: xItem, S: "Invoice #: GW-20418"}, pdftest.Cell{X: xRight, S: "Invoice Date: 04/15/2026"})
		p.Add(654, pdftest.Cell{X: xItem, S: "P.O.#: EP-5521"}, pdftest.Cell{X: xRight, S: "Terms: Net 30"})
		p.Add(630, pdftest.Cell{X: xItem, S: "Bill To:"})
		for i := range ownBlock {
			p.Add(614-float64(i)*rowGap, pdftest.Cell{X: xItem, S: ownBlock[i]})
		}
		poTable(&p, yTable-40, lines, []kv{{"Subtotal", "$2,980.00"}, {"Sales Tax", "$245.85"}, {"Total", "$3,225.85"}})
		return []pdftest.Page{p}
	}}
}

// billMultiPageSpec: a two-page bill with a letter-spaced "I N V O I C E"
// title whose number sits in a box under it (the letterhead shares the title's
// row), freight and fuel rows, a "Less deposit" row and an explicit due date.
func billMultiPageSpec() docSpec {
	page1 := []lineSpec{
		{"SL-101", "Quartzite slab Taj Mahal", "2", "EA", "$1,450.00", "$2,900.00", "", false},
		{"SL-102", "Quartzite slab Fantasy Brown", "1", "EA", "$1,380.0000", "$1,380.00", "", false},
		{"SK-7", "Undermount sink bowl", "3", "EA", "$129.5000", "$388.50", "", false},
	}
	page2 := []lineSpec{
		{"AD-5", "Epoxy adhesive kit", "6", "EA", "$22.00", "$132.00", "", false},
		{"", "Freight", "", "", "", "$180.00", "", false},
		{"", "Fuel surcharge", "", "", "", "$24.00", "", false},
		{"", "Less deposit received", "", "", "", "-$1,000.00", "", false},
	}
	exp := expectedDoc{
		Header: vendorHdr("Stonebridge Imports Inc", map[string]string{
			"invoice_number": "SB-770215", "invoice_date": "2026-06-02", "due_date": "2026-07-17", "po_number": "EP-5602",
			"payment_terms": "Net 45", "shipping": "204.00", "subtotal": "4800.50", "total": "5004.50",
		}),
		Lines: expLines(append(append([]lineSpec{}, page1...), page2[0])...), Class: "",
	}
	return docSpec{kind: kindVendorBill, name: "02_bill_multipage_freight_deposit", exp: exp, raw: func() []pdftest.Page {
		var p1 pdftest.Page
		p1.Add(750, pdftest.Cell{X: xItem, S: "Stonebridge Imports Inc"}, pdftest.Cell{X: 430, S: "I N V O I C E"})
		p1.Add(738, pdftest.Cell{X: xItem, S: "410 Harbor Blvd"})
		p1.Add(726, pdftest.Cell{X: xItem, S: "Houston, TX 77002"}, pdftest.Cell{X: 430, S: "SB-770215"})
		p1.Add(690, pdftest.Cell{X: xItem, S: "Date: 06/02/2026"}, pdftest.Cell{X: xRight, S: "Your PO: EP-5602"})
		p1.Add(674, pdftest.Cell{X: xItem, S: "Terms: Net 45"}, pdftest.Cell{X: xRight, S: "Due Date: 07/17/2026"})
		p1.Add(650, pdftest.Cell{X: xItem, S: "Sold To:"}, pdftest.Cell{X: xRight, S: "Remit To:"})
		remit := []string{"Stonebridge Imports Inc", "PO Box 4410", "Houston, TX 77210"}
		for i := range ownBlock {
			p1.Add(634-float64(i)*rowGap, pdftest.Cell{X: xItem, S: ownBlock[i]}, pdftest.Cell{X: xRight, S: remit[i]})
		}
		p1.Add(yTable-40, tableHeader()...)
		addRows(&p1, yTable-40-rowGap, page1)
		p1.Add(footerY, pdftest.Cell{X: 280, S: "Page 1 of 2"})
		var p2 pdftest.Page
		p2.Add(740, tableHeader()...)
		y := addRows(&p2, 740-rowGap, page2) - rowGap
		for _, t := range []kv{{"Subtotal", "$4,800.50"}, {"Freight", "$204.00"}, {"Invoice Total", "$5,004.50"}, {"Balance Due", "$4,004.50"}} {
			p2.Add(y, pdftest.Cell{X: xLabel, S: t.Label}, pdftest.Right(xAmount, t.Amount))
			y -= rowGap
		}
		p2.Add(footerY, pdftest.Cell{X: 280, S: "Page 2 of 2"})
		return []pdftest.Page{p1, p2}
	}}
}

// statementSpec: an account statement uploaded as a bill. It must be flagged
// as the wrong document, never read as a single bill and never sent to the model.
func statementSpec() docSpec {
	exp := expectedDoc{
		Header: map[string]string{"currency": "USD"}, Class: "statement",
		Warnings: []string{"document_looks_like_statement"},
	}
	return docSpec{kind: kindVendorBill, name: "03_statement_mismatch", exp: exp, raw: func() []pdftest.Page {
		var p pdftest.Page
		p.Add(750, pdftest.Cell{X: xItem, S: "STATEMENT"})
		p.Add(734, pdftest.Cell{X: xItem, S: "Granite Works LLC"})
		p.Add(722, pdftest.Cell{X: xItem, S: "900 Quarry Rd, Suite 10"})
		p.Add(690, pdftest.Cell{X: xItem, S: "Statement Date: 06/30/2026"})
		p.Add(650, pdftest.Cell{X: xItem, S: "Invoice"}, pdftest.Cell{X: 200, S: "Date"}, pdftest.Right(xAmount, "Balance"))
		rows := [][3]string{{"GW-20418", "04/15/2026", "$3,225.85"}, {"GW-20502", "05/20/2026", "$1,118.00"}, {"GW-20611", "06/11/2026", "$640.25"}}
		y := 634.0
		for _, r := range rows {
			p.Add(y, pdftest.Cell{X: xItem, S: r[0]}, pdftest.Cell{X: 200, S: r[1]}, pdftest.Right(xAmount, r[2]))
			y -= rowGap
		}
		p.Add(y-rowGap, pdftest.Cell{X: xLabel, S: "Balance Due"}, pdftest.Right(xAmount, "$4,984.10"))
		return []pdftest.Page{p}
	}}
}

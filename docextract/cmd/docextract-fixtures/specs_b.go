package main

func specsB() []docSpec {
	freight := []lineSpec{
		{"GR-600", "Granite slab", "5", "EA", "$700.00", "$3,500.00", "", false},
		{"TK-1", "Trim kit", "2", "EA", "$30.00", "$60.00", "", false},
		{"", "Freight", "", "", "", "$150.00", "", false},
		{"", "Discount", "", "", "", "-$175.00", "", false},
	}
	quoted := []lineSpec{
		{"QZ-900", "Quartz slab 30mm", "6", "EA", "$510.00", "$3,060.00", "", false},
		{"QZ-901", "Quartz offcut", "2", "EA", "$75.00", "$150.00", "", false},
	}
	mismatch := []lineSpec{
		{"A-1", "Granite sample set", "5", "EA", "$100.00", "$500.00", "", false},
		{"A-2", "Marble sample set", "4", "EA", "$122.50", "$490.00", "", false},
	}
	docxLines := []lineSpec{
		{"GR-710", "Absolute black granite", "3", "EA", "$640.00", "$1,920.00", "", false},
		{"GR-711", "Silver cloud granite", "2", "EA", "$505.00", "$1,010.00", "", false},
	}
	sqm := []lineSpec{
		{"QZ-20", "Quartz slab 20mm", "12.5", "SQM", "$140.00", "$1,750.00", "", false},
		{"PC-80", "Porcelain panel", "8", "SQM", "$95.40", "$763.20", "", false},
	}
	strip := []lineSpec{
		{"LM-5", "Limestone paver", "40", "EA", "$11.25", "$450.00", "", false},
		{"LM-6", "Limestone coping", "10", "EA", "$24.00", "$240.00", "", false},
	}
	return []docSpec{
		{name: "05_freight_and_discount", title: "PURCHASE ORDER", poLine: "PO Number: 6105", dateLine: "Order Date: 04/01/2026", termsLine: "Terms: Net 30",
			bill: billAcme, ship: shipAcme, lines: freight,
			totals: []kv{{"Subtotal", "$3,560.00"}, {"Total", "$3,535.00"}},
			exp:    expectedDoc{Header: hdr("6105", "2026-04-01", "", "Net 30", "ACME Stone Inc", map[string]string{"subtotal": "3560.00", "shipping": "150.00", "discount": "175.00", "total": "3535.00"}), Lines: expLines(freight[:2]...)}},
		{name: "06_revision_2", title: "PURCHASE ORDER", top: []string{"Revision 2", "Supersedes PO 4400"}, poLine: "PO Number: 4471-R2", dateLine: "Order Date: 01/20/2026", termsLine: "Terms: Net 30",
			bill: billAcme, ship: shipAcme, lines: quoted[:1],
			totals: []kv{{"Subtotal", "$3,060.00"}, {"Total", "$3,060.00"}},
			exp:    expectedDoc{Header: hdr("4471-R2", "2026-01-20", "", "Net 30", "ACME Stone Inc", map[string]string{"subtotal": "3060.00", "total": "3060.00"}), Lines: expLines(quoted[:1]...), Revision: "Revision 2|4400"}},
		{name: "07_quote_reference", title: "PURCHASE ORDER", top: []string{"Your Quote # Q-1001"}, poLine: "PO Number: 9034", dateLine: "Order Date: 05/05/2026", termsLine: "Terms: Net 30",
			bill: []string{"Harbor Design Group", "40 Pier Avenue", "Seattle, WA 98101"}, ship: shipAcme, lines: quoted,
			totals: []kv{{"Subtotal", "$3,210.00"}, {"Total", "$3,210.00"}},
			exp:    expectedDoc{Header: hdr("9034", "2026-05-05", "", "Net 30", "Harbor Design Group", map[string]string{"subtotal": "3210.00", "total": "3210.00"}), Lines: expLines(quoted...), QuoteRef: "Q-1001"}},
		{name: "08_totals_mismatch", title: "PURCHASE ORDER", poLine: "PO Number: 3310", dateLine: "Order Date: 06/06/2026", termsLine: "Terms: Net 30",
			bill: billAcme, ship: shipAcme, lines: mismatch,
			totals: []kv{{"Subtotal", "$1,000.00"}, {"Total", "$1,000.00"}},
			exp:    expectedDoc{Header: hdr("3310", "2026-06-06", "", "Net 30", "ACME Stone Inc", map[string]string{"subtotal": "1000.00", "total": "1000.00"}), Lines: expLines(mismatch...)}},
		{name: "09_docx_table", docx: true, title: "PURCHASE ORDER", poLine: "PO Number: 7001", dateLine: "Order Date: 07/07/2026", termsLine: "Terms: Net 30",
			bill: []string{"Granite Works Co", "7 Mill Lane", "Tulsa, OK 74103"}, lines: docxLines,
			totals: []kv{{"Subtotal", "$2,930.00"}, {"Total", "$2,930.00"}},
			exp:    expectedDoc{Header: hdr("7001", "2026-07-07", "", "Net 30", "Granite Works Co", map[string]string{"subtotal": "2930.00", "total": "2930.00"}), Lines: expLines(docxLines...)}},
		{name: "10_square_meter_units", title: "PURCHASE ORDER", poLine: "PO Number: 2208", dateLine: "Order Date: 08/12/2026", termsLine: "Terms: Net 60",
			bill: billAcme, ship: shipAcme, lines: sqm,
			totals: []kv{{"Subtotal", "$2,513.20"}, {"Total", "$2,513.20"}},
			exp:    expectedDoc{Header: hdr("2208", "2026-08-12", "", "Net 60", "ACME Stone Inc", map[string]string{"subtotal": "2513.20", "total": "2513.20"}), Lines: expLines(sqm...)}},
		{name: "11_label_strip_header", title: "PURCHASE ORDER", strip: true, stripVals: [4]string{"A-77391", "09/09/2026", "Net 45", "February 14, 2026"},
			customer: "Bedrock Builders LLC", bill: []string{"88 Industrial Way", "Austin, TX 78701"}, ship: shipAcme, lines: strip,
			totals: []kv{{"Subtotal", "$690.00"}, {"Total", "$690.00"}},
			exp:    expectedDoc{Header: hdr("A-77391", "2026-09-09", "2026-02-14", "Net 45", "Bedrock Builders LLC", map[string]string{"subtotal": "690.00", "total": "690.00"}), Lines: expLines(strip...)}},
	}
}

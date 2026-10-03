package main

var (
	billAcme = []string{"ACME Stone Inc", "12 Main Street", "Dallas, TX 75201"}
	shipAcme = []string{"ACME Stone - Plano Yard", "900 Quarry Road", "Plano, TX 75024"}
)

func hdr(po, date, delivery, terms, name string, extra map[string]string) map[string]string {
	h := map[string]string{"po_number": po, "order_date": date, "customer_name": name, "currency": "USD"}
	if delivery != "" {
		h["delivery_date"] = delivery
	}
	if terms != "" {
		h["payment_terms"] = terms
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func expLines(ls ...lineSpec) []expectedLine {
	out := make([]expectedLine, len(ls))
	for i, l := range ls {
		desc := l.Desc
		if l.Wrap != "" {
			desc += " " + l.Wrap
		}
		out[i] = expectedLine{SKU: l.SKU, Description: desc, Qty: l.Qty, UoM: l.UoM, UnitPrice: plain(l.Price), Amount: plain(l.Amount)}
	}
	return out
}

// plain turns "$1,234.50" into "1234.50".
func plain(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '$' && s[i] != ',' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func allSpecs() []docSpec {
	return append(specsA(), specsB()...)
}

func specsA() []docSpec {
	simple := []lineSpec{
		{"GR-100", "Black Galaxy granite slab", "10", "SF", "$12.50", "$125.00", "", false},
		{"QZ-200", "Calacatta quartz slab", "4", "EA", "$450.00", "$1,800.00", "", false},
		{"TL-300", "Tile adhesive 25kg bag", "6", "EA", "$18.25", "$109.50", "", false},
	}
	wrapped := []lineSpec{
		{"MB-410", "Imperial White marble countertop slab 3cm", "8", "EA", "$620.00", "$4,960.00", "honed finish, two slabs per bundle", false},
		{"MB-411", "Carrara marble vanity top", "3", "EA", "$275.00", "$825.00", "", false},
	}
	twoPage := []lineSpec{
		{"ST-1", "Soapstone slab", "2", "EA", "$900.00", "$1,800.00", "", false},
		{"ST-2", "Slate tile pack", "10", "EA", "$35.00", "$350.00", "", false},
		{"ST-3", "Epoxy adhesive", "5", "EA", "$22.00", "$110.00", "", false},
		{"ST-4", "Quartzite slab", "1", "EA", "$1,200.00", "$1,200.00", "", false},
		{"ST-5", "Diamond blade 7in", "2", "EA", "$64.50", "$129.00", "", false},
		{"ST-6", "Safety gloves", "12", "EA", "$4.75", "$57.00", "", false},
	}
	addons := []lineSpec{
		{"GR-500", "Kitchen island granite top", "1", "EA", "$2,400.00", "$2,400.00", "", false},
		{"", "Eased edge profile", "1", "EA", "$150.00", "$150.00", "", true},
		{"", "+ Sink cutout", "1", "EA", "$85.00", "$85.00", "", true},
		{"GR-501", "Perimeter granite top", "1", "EA", "$1,800.00", "$1,800.00", "", false},
		{"", "w/ Installation", "1", "EA", "$400.00", "$400.00", "", true},
	}
	return []docSpec{
		{name: "01_simple_table", title: "PURCHASE ORDER", poLine: "PO Number: 4471", dateLine: "Order Date: 01/02/2026", termsLine: "Terms: Net 30",
			bill: billAcme, ship: shipAcme, lines: simple,
			totals: []kv{{"Subtotal", "$2,034.50"}, {"Sales Tax", "$167.85"}, {"Total", "$2,202.35"}},
			exp:    expectedDoc{Header: hdr("4471", "2026-01-02", "", "Net 30", "ACME Stone Inc", map[string]string{"subtotal": "2034.50", "tax": "167.85", "total": "2202.35"}), Lines: expLines(simple...)}},
		{name: "02_wrapped_descriptions", title: "PURCHASE ORDER", poLine: "PO No. PO-7782", dateLine: "Date: 03/15/2026", termsLine: "Terms: Net 45",
			bill: []string{"Bedrock Builders LLC", "88 Industrial Way", "Austin, TX 78701"}, ship: shipAcme, lines: wrapped,
			totals: []kv{{"Subtotal", "$5,785.00"}, {"Total", "$5,785.00"}},
			exp:    expectedDoc{Header: hdr("PO-7782", "2026-03-15", "", "Net 45", "Bedrock Builders LLC", map[string]string{"subtotal": "5785.00", "total": "5785.00"}), Lines: expLines(wrapped...)}},
		{name: "03_two_pages_repeated_header", title: "PURCHASE ORDER", poLine: "P.O. # 88-1203", dateLine: "Date: 1/2/26", termsLine: "Terms: Net 30",
			bill: []string{"Summit Kitchens", "5 Ridge Road", "Denver, CO 80202"}, ship: shipAcme, lines: twoPage, pageSplit: 3,
			totals: []kv{{"Subtotal", "$3,646.00"}, {"Total", "$3,646.00"}},
			exp:    expectedDoc{Header: hdr("88-1203", "2026-01-02", "", "Net 30", "Summit Kitchens", map[string]string{"subtotal": "3646.00", "total": "3646.00"}), Lines: expLines(twoPage...)}},
		{name: "04_addon_lines", title: "PURCHASE ORDER", poLine: "PO Number: 5120", dateLine: "Order Date: 02/10/2026", termsLine: "Terms: Net 30",
			bill: billAcme, ship: shipAcme, lines: addons,
			totals: []kv{{"Subtotal", "$4,835.00"}, {"Total", "$4,835.00"}},
			exp:    expectedDoc{Header: hdr("5120", "2026-02-10", "", "Net 30", "ACME Stone Inc", map[string]string{"subtotal": "4835.00", "total": "4835.00"}), Lines: expLines(addons...)}},
	}
}

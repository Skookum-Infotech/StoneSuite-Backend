package main

import "stonesuite-backend/docextract/internal/pdftest"

// Countertop order form (a builder's fillable spec sheet), laid out like the
// real ones: the title and job labels sit in a form XObject (an edit layer
// the plain page reader can't see, which also repeats one label a point
// off), area boxes are printed in two columns, and every value is a filled
// widget: text fields, dropdowns (one exporting "" but showing 4"), and a
// faucet-spread tick. An empty area box must produce no line. Names, numbers
// and the address are made up.

const (
	ofLeftCol  = 42.0
	ofRightCol = 312.0
)

func orderFormSpec() docSpec {
	exp := expectedDoc{
		Header: hdr("", "", "2026-11-13", "", "Lakeview Homes", map[string]string{
			"ship_to": "418 Willow Bend\nCelina 75009",
			"notes": "Special instructions: NO POP-UP OUTLET\n" +
				"Builder contact: Dana Reyes, 214-555-0142, dana.reyes@lakeviewhomes.com\n" +
				"Community: Brookfield. Plan: Aspen-C. Measure date: 11/3/26.",
		}),
		Lines: []expectedLine{
			{Description: "CALACATTA LUX", Qty: "1"},
			{Description: "CALACATTA LUX", Qty: "1"},
			{SKU: "440185", Description: "440185 DOUBLE WHITE", Qty: "1"},
		},
	}
	return docSpec{name: "14_countertop_order_form", exp: exp, raw: orderFormPages}
}

func txt(x, y float64, s string) pdftest.Text { return pdftest.Text{X: x, Y: y, S: s} }

func field(typ, value string, x0, y0, x1, y1 float64) pdftest.Field {
	return pdftest.Field{Rect: [4]float64{x0, y0, x1, y1}, Type: typ, Value: value}
}

func orderFormPages() []pdftest.Page {
	var p pdftest.Page
	areaBox := func(y float64, left, right string) {
		p.Add(y, pdftest.Cell{X: ofLeftCol, S: left}, pdftest.Cell{X: ofRightCol, S: right})
		p.Add(y-17, pdftest.Cell{X: ofLeftCol, S: "Color"}, pdftest.Cell{X: ofRightCol, S: "Color"})
		p.Add(y-33, pdftest.Cell{X: ofLeftCol, S: "Finish"}, pdftest.Cell{X: 165, S: "Edge"},
			pdftest.Cell{X: ofRightCol, S: "Finish"}, pdftest.Cell{X: 433, S: "Edge"})
		p.Add(y-48, pdftest.Cell{X: ofLeftCol, S: "Backsplash"}, pdftest.Cell{X: 115, S: "Height"},
			pdftest.Cell{X: ofRightCol, S: "Sink Model"})
		p.Add(y-63, pdftest.Cell{X: ofLeftCol, S: "Sink Model"}, pdftest.Cell{X: ofRightCol, S: "Cooktop Model"})
		p.Add(y-77, pdftest.Cell{X: ofLeftCol, S: "Cooktop Model"}, pdftest.Cell{X: 201, S: "Waterfall"},
			pdftest.Cell{X: 262, S: "Qty"}, pdftest.Cell{X: ofRightCol, S: "Waterfall"}, pdftest.Cell{X: 375, S: "Qty"})
		p.Add(y-95, pdftest.Cell{X: 141, S: "Faucet Spread"}, pdftest.Cell{X: 412, S: "Faucet Spread"})
		marks := []string{`12"`, `8"`, `4"`, "C", `4"`, `8"`, `12"`}
		var cells []pdftest.Cell
		for i, m := range marks {
			cells = append(cells, pdftest.Cell{X: 48 + float64(i)*38, S: m}, pdftest.Cell{X: 318 + float64(i)*38, S: m})
		}
		p.Add(y-109, cells...)
		p.Add(y-136, pdftest.Cell{X: ofLeftCol, S: "Notes"}, pdftest.Cell{X: ofRightCol, S: "Notes"})
	}
	areaBox(634, "KITCHEN", "ISLAND")
	areaBox(482, "PANTRY", "HUTCH")

	p.Layers = []pdftest.Layer{{Texts: []pdftest.Text{
		txt(166, 755, "COUNTERTOP ORDER FORM"),
		txt(38, 736, "Builder"), txt(204, 736, "Community"), txt(474, 736, "Plan"),
		txt(38, 711, "Phone"), txt(161, 711, "Install Address"), txt(443, 711, "Measure Date"),
		txt(38, 686, "Email"), txt(246, 686, "City / Zip"), txt(457, 686, "Install Date"),
		txt(39, 668, "Special Instructions"),
		txt(129, 668, "**For specific area information please use the notes field in the area box**"),
		// The edit layer repeats the Island's label a point and a half off.
		txt(ofRightCol+1.5, 586.9, "Sink Model"),
	}}}

	heightOpts := [][2]string{{" ", " "}, {"", `4"`}, {"6", `6"`}}
	height := field(pdftest.FieldChoice, "", 150, 582, 190, 596)
	height.Options, height.Selected = heightOpts, []int{1}
	blankHeight := field(pdftest.FieldChoice, "", 150, 430, 190, 444)
	blankHeight.Options = heightOpts
	faucet := field(pdftest.FieldCheckbox, "", 434, 514, 446, 526)
	faucet.Checked = true
	p.Fields = []pdftest.Field{
		field(pdftest.FieldText, "Dana Reyes", 72, 732, 190, 746),
		field(pdftest.FieldText, "Brookfield", 260, 732, 400, 746),
		field(pdftest.FieldText, "Aspen-C", 496, 732, 580, 746),
		field(pdftest.FieldText, "214-555-0142", 72, 707, 150, 721),
		field(pdftest.FieldText, "418 Willow Bend", 234, 707, 430, 721),
		field(pdftest.FieldText, "11/3/26", 512, 707, 580, 721),
		field(pdftest.FieldText, "dana.reyes@lakeviewhomes.com", 68, 682, 240, 696),
		field(pdftest.FieldText, "Celina / 75009", 290, 682, 440, 696),
		field(pdftest.FieldText, "11/13/26", 512, 682, 580, 696),
		field(pdftest.FieldText, "NO POP-UP OUTLET", 38, 650, 560, 664),
		// Kitchen.
		field(pdftest.FieldText, "CALACATTA LUX", 66, 613, 200, 627),
		field(pdftest.FieldChoice, "POLISHED", 70, 597, 160, 611),
		field(pdftest.FieldChoice, "EASED", 190, 597, 300, 611),
		height,
		field(pdftest.FieldText, "CT-3648", 113, 553, 190, 567),
		// Island.
		field(pdftest.FieldText, "CALACATTA LUX", 338, 613, 470, 627),
		field(pdftest.FieldChoice, "POLISHED", 340, 597, 430, 611),
		field(pdftest.FieldChoice, "HALF BULLNOSE", 458, 597, 570, 611),
		field(pdftest.FieldChoice, "440185 DOUBLE WHITE", 368, 582, 570, 596),
		faucet,
		// Pantry: an untouched dropdown, so no line.
		blankHeight,
	}
	return []pdftest.Page{p}
}

package main

import (
	"strings"

	"stonesuite-backend/docextract/internal/pdftest"
)

// Column right edges / left starts of the standard line table.
const (
	xItem   = 50.0
	xDesc   = 110.0
	xQty    = 350.0
	xUoM    = 362.0
	xPrice  = 460.0
	xAmount = 545.0
	xLabel  = 440.0
	xRight  = 330.0

	yTitle     = 750.0
	yPO        = 700.0
	yBill      = 650.0
	yTable     = 560.0
	rowGap     = 16.0
	wrapGap    = 12.0
	addonShift = 14.0
	footerY    = 40.0
)

// lineSpec is one document line; Wrap is an optional continuation row and
// Indent shifts the description right (add-ons).
type lineSpec struct {
	SKU, Desc, Qty, UoM, Price, Amount string
	Wrap                               string
	Indent                             bool
}

type kv struct{ Label, Amount string }

// docSpec describes one fixture; its expected output is stated explicitly.
type docSpec struct {
	name      string
	docx      bool
	title     string
	top       []string // extra left lines under the title (revision, quote ref)
	poLine    string
	dateLine  string
	termsLine string
	strip     bool // label-above-value header strip instead of inline labels
	stripVals [4]string
	customer  string // inline "Customer:" label instead of Bill To when set
	bill      []string
	ship      []string
	lines     []lineSpec
	pageSplit int // lines on page one (0 = single page)
	notes     []string
	totals    []kv
	exp       expectedDoc
}

type expectedDoc struct {
	Header   map[string]string `json:"header"`
	Lines    []expectedLine    `json:"lines"`
	Revision string            `json:"revision,omitempty"`
	QuoteRef string            `json:"quote_ref,omitempty"`
}

type expectedLine struct {
	SKU         string `json:"sku"`
	Description string `json:"description"`
	Qty         string `json:"qty"`
	UoM         string `json:"uom"`
	UnitPrice   string `json:"unit_price"`
	Amount      string `json:"amount"`
}

func (s docSpec) expected() expectedDoc { return s.exp }

func (s docSpec) render() (string, []byte) {
	if s.docx {
		return s.name + ".docx", pdftest.BuildDocx(s.docxBlocks())
	}
	return s.name + ".pdf", pdftest.Build(s.pages(), pdftest.Options{})
}

func (s docSpec) pages() []pdftest.Page {
	var p pdftest.Page
	p.Add(yTitle, pdftest.Cell{X: xItem, S: s.title})
	y := yTitle - rowGap
	for _, t := range s.top {
		p.Add(y, pdftest.Cell{X: xItem, S: t})
		y -= rowGap
	}
	s.headerBlock(&p, y) // the table starts at the fixed yTable, not below the header
	rows := s.lines
	var second []lineSpec
	if s.pageSplit > 0 && s.pageSplit < len(rows) {
		rows, second = rows[:s.pageSplit], rows[s.pageSplit:]
	}
	pages := []pdftest.Page{p}
	cur := &pages[0]
	cur.Add(yTable, tableHeader()...)
	y = yTable - rowGap
	y = addRows(cur, y, rows)
	if second != nil {
		cur.Add(footerY, pdftest.Cell{X: 280, S: "Page 1 of 2"})
		pages = append(pages, pdftest.Page{})
		cur = &pages[1]
		cur.Add(740, tableHeader()...)
		y = 740 - rowGap
		y = addRows(cur, y, second)
	}
	for _, n := range s.notes {
		cur.Add(y, pdftest.Cell{X: xDesc, S: n})
		y -= rowGap
	}
	y -= rowGap
	for _, t := range s.totals {
		cur.Add(y, pdftest.Cell{X: xLabel, S: t.Label}, pdftest.Right(xAmount, t.Amount))
		y -= rowGap
	}
	if second != nil {
		cur.Add(footerY, pdftest.Cell{X: 280, S: "Page 2 of 2"})
	}
	return pages
}

// headerBlock draws PO/date/terms and the party blocks; it returns the next y.
func (s docSpec) headerBlock(p *pdftest.Page, y float64) float64 {
	if s.strip {
		p.Add(yPO, pdftest.Cell{X: xItem, S: "PO NUMBER"}, pdftest.Cell{X: 200, S: "ORDER DATE"}, pdftest.Cell{X: 350, S: "TERMS"}, pdftest.Cell{X: 450, S: "DELIVERY DATE"})
		p.Add(yPO-rowGap, pdftest.Cell{X: xItem, S: s.stripVals[0]}, pdftest.Cell{X: 200, S: s.stripVals[1]}, pdftest.Cell{X: 350, S: s.stripVals[2]}, pdftest.Cell{X: 450, S: s.stripVals[3]})
	} else {
		p.Add(yPO, pdftest.Cell{X: xItem, S: s.poLine}, pdftest.Cell{X: xRight, S: s.dateLine})
		if s.termsLine != "" {
			p.Add(yPO-rowGap, pdftest.Cell{X: xRight, S: s.termsLine})
		}
	}
	if s.customer != "" {
		p.Add(yBill, pdftest.Cell{X: xItem, S: "Customer: " + s.customer}, pdftest.Cell{X: xRight, S: "Ship To:"})
	} else {
		p.Add(yBill, pdftest.Cell{X: xItem, S: "Bill To:"}, pdftest.Cell{X: xRight, S: "Ship To:"})
	}
	by := yBill - rowGap
	for i := 0; i < len(s.bill) || i < len(s.ship); i++ {
		if i < len(s.bill) {
			p.Add(by, pdftest.Cell{X: xItem, S: s.bill[i]})
		}
		if i < len(s.ship) {
			p.Add(by, pdftest.Cell{X: xRight, S: s.ship[i]})
		}
		by -= rowGap
	}
	return by
}

func tableHeader() []pdftest.Cell {
	return []pdftest.Cell{
		{X: xItem, S: "Item"}, {X: xDesc, S: "Description"}, pdftest.Right(xQty, "Qty"),
		{X: xUoM, S: "UoM"}, pdftest.Right(xPrice, "Unit Price"), pdftest.Right(xAmount, "Amount"),
	}
}

func addRows(p *pdftest.Page, y float64, rows []lineSpec) float64 {
	for _, l := range rows {
		dx := 0.0
		if l.Indent {
			dx = addonShift
		}
		cells := []pdftest.Cell{{X: xItem, S: l.SKU}, {X: xDesc + dx, S: l.Desc}}
		if l.Qty != "" {
			cells = append(cells, pdftest.Right(xQty, l.Qty))
		}
		if l.UoM != "" {
			cells = append(cells, pdftest.Cell{X: xUoM, S: l.UoM})
		}
		if l.Price != "" {
			cells = append(cells, pdftest.Right(xPrice, l.Price))
		}
		cells = append(cells, pdftest.Right(xAmount, l.Amount))
		p.Add(y, nonEmpty(cells)...)
		if l.Wrap != "" {
			y -= wrapGap
			p.Add(y, pdftest.Cell{X: xDesc, S: l.Wrap})
		}
		y -= rowGap
	}
	return y
}

func nonEmpty(cs []pdftest.Cell) []pdftest.Cell {
	var out []pdftest.Cell
	for _, c := range cs {
		if strings.TrimSpace(c.S) != "" {
			out = append(out, c)
		}
	}
	return out
}

func (s docSpec) docxBlocks() []pdftest.DocxBlock {
	b := []pdftest.DocxBlock{{Para: s.title}, {Para: s.poLine}, {Para: s.dateLine}}
	if s.termsLine != "" {
		b = append(b, pdftest.DocxBlock{Para: s.termsLine})
	}
	b = append(b, pdftest.DocxBlock{Para: "Bill To:"})
	for _, l := range s.bill {
		b = append(b, pdftest.DocxBlock{Para: l})
	}
	tbl := [][][]string{{{"Item"}, {"Description"}, {"Qty"}, {"UoM"}, {"Unit Price"}, {"Amount"}}}
	for _, l := range s.lines {
		tbl = append(tbl, [][]string{{l.SKU}, {l.Desc}, {l.Qty}, {l.UoM}, {l.Price}, {l.Amount}})
	}
	b = append(b, pdftest.DocxBlock{Table: tbl})
	for _, t := range s.totals {
		b = append(b, pdftest.DocxBlock{Para: t.Label + " " + t.Amount})
	}
	return b
}

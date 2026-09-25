package docpdf

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleDoc() PrintableDoc {
	return PrintableDoc{
		Seller:         Seller{Name: "Acme Stone Co", AddrLine1: "1 Quarry Rd", CityStateZip: "Austin, TX 78701", Email: "sales@acme.example"},
		Kind:           "INVOICE",
		Number:         "INV-1001",
		Status:         "Sent",
		IssueDate:      "2026-08-24",
		DueDate:        "2026-09-23",
		BillTo:         Address{Name: "Bob Buyer", Line1: "22 Main St", CityStateZip: "Dallas, TX 75201", Email: "bob@buyer.example"},
		CurrencySymbol: "$",
		Lines: []PrintLine{
			{SKU: "SLAB-1", Name: "Granite Slab", Description: "Absolute black, polished", UnitCode: "ea", Quantity: 3, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 750},
		},
		Subtotal: 750, TaxTotal: 61.88, GrandTotal: 811.88,
		Terms: "Net 30", Notes: "Thank you for your business.",
	}
}

func samplePNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 80))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PrintableDoc)
		wantErr bool
	}{
		{"valid", func(*PrintableDoc) {}, false},
		{"missing kind", func(d *PrintableDoc) { d.Kind = "" }, true},
		{"missing number", func(d *PrintableDoc) { d.Number = "" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDoc()
			tc.mutate(&d)
			err := d.Validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDrawAddress_WrapsLongLines(t *testing.T) {
	pdf := newDoc()
	startY := pdf.GetY()
	long := "VAKARANI CAMP, SIRUGUPPA ROAD NEAR HULIGEMMA TEMPLE, BELLARY DISTRICT, KARNATAKA 583101"
	drawAddress(pdf, marginX, startY, "BILL TO", Address{Name: "Akhila N", Line1: long})
	endY := pdf.GetY()
	unwrappedHeight := partyBodyDY + partyNameH + partyLineH // label block + Name + Line1, if Line1 stayed a single line
	assert.Greater(t, endY-startY, unwrappedHeight, "long address line must wrap within the column instead of overflowing past it into the neighboring column")
}

func TestRender(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *PrintableDoc)
	}{
		{"basic", func(*testing.T, *PrintableDoc) {}},
		{"no lines", func(_ *testing.T, d *PrintableDoc) { d.Lines = nil }},
		{"with balance", func(_ *testing.T, d *PrintableDoc) { d.ShowBalance = true; d.AmountPaid = 100; d.BalanceDue = 711.88 }},
		{"with tenant logo", func(t *testing.T, d *PrintableDoc) { d.Seller.LogoPNG = samplePNGBytes(t) }},
		{"with invalid tenant logo", func(_ *testing.T, d *PrintableDoc) { d.Seller.LogoPNG = []byte("not a real image") }},
		{"long kind and number", func(_ *testing.T, d *PrintableDoc) {
			d.Kind, d.Number = "PURCHASE ORDER RETURN AUTHORIZATION", "PORA-2026-0000000000123456789"
		}},
		{"non-latin text does not break the render", func(_ *testing.T, d *PrintableDoc) {
			d.Seller.Name = "Café Müller — 日本 ₹"
			d.Lines[0].Name = "Granite “Slab” — Ünïcode"
			d.CurrencySymbol = "₹"
		}},
		{"no parties, no notes", func(_ *testing.T, d *PrintableDoc) {
			d.BillTo, d.ShipTo = Address{}, Address{}
			d.Terms, d.Notes, d.Memo = "", "", ""
		}},
		{"multipage", func(_ *testing.T, d *PrintableDoc) {
			d.Lines = nil
			for i := 0; i < 80; i++ {
				d.Lines = append(d.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", UnitCode: "ea", Quantity: 1, UnitPrice: 10, LineTotal: 10})
			}
		}},
		{"full seller and both addresses", func(_ *testing.T, d *PrintableDoc) {
			d.Seller = Seller{
				Name:         "Acme Stone Co",
				AddrLine1:    "1 Quarry Rd, Suite 200",
				AddrLine2:    "Building B, Loading Dock 4",
				CityStateZip: "Austin, TX 78701",
				Phone:        "(512) 555-0100",
				Email:        "sales@acme.example",
			}
			d.BillTo = Address{
				Name:         "Bob Buyer",
				Attention:    "Attn: Accounts Payable",
				Line1:        "22 Main St",
				Line2:        "Suite 400",
				CityStateZip: "Dallas, TX 75201",
				Phone:        "(214) 555-0199",
				Email:        "bob@buyer.example",
			}
			d.ShipTo = Address{
				Name:         "Bob Buyer Warehouse",
				Attention:    "Attn: Receiving Dock",
				Line1:        "500 Industrial Pkwy",
				Line2:        "Dock 12",
				CityStateZip: "Fort Worth, TX 76102",
				Phone:        "(817) 555-0177",
				Email:        "receiving@buyer.example",
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDoc()
			tc.mutate(t, &d)
			out, err := Render(d)
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(out, []byte("%PDF-")), "output must be a PDF")
			assert.Greater(t, len(out), 500)
		})
	}
}

var pageObjRE = regexp.MustCompile(`/Type /Page[^s]`)

func TestRender_PageCount(t *testing.T) {
	many := sampleDoc()
	many.Lines = nil
	for i := 0; i < 80; i++ {
		many.Lines = append(many.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", Quantity: 1, UnitPrice: 10, LineTotal: 10})
	}
	tests := []struct {
		name string
		doc  PrintableDoc
		want func(pages int) bool
	}{
		{"short document is one page", sampleDoc(), func(n int) bool { return n == 1 }},
		{"long document paginates", many, func(n int) bool { return n >= 2 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(tc.doc)
			require.NoError(t, err)
			pages := len(pageObjRE.FindAll(out, -1))
			assert.True(t, tc.want(pages), "unexpected page count %d", pages)
		})
	}
}

func TestRender_EmbedsLogos(t *testing.T) {
	imageObjects := func(d PrintableDoc) int {
		out, err := Render(d)
		require.NoError(t, err)
		return bytes.Count(out, []byte("/Subtype /Image"))
	}
	without := imageObjects(sampleDoc())
	assert.Positive(t, without, "the StoneSuite mark is on every document")

	d := sampleDoc()
	d.Seller.LogoPNG = samplePNGBytes(t)
	assert.Greater(t, imageObjects(d), without, "the tenant logo adds a second image")
}

// The line-table header and the running footer must appear on every page of a
// long document, not just the first -- eyeballing a rendered sample can't
// prove this for page 3+ (the browser preview only shows page 1), so these
// assert it directly from the generated PDF's own content.
func TestRender_TableHeaderRepeatsOnContinuationPages(t *testing.T) {
	d := sampleDoc()
	d.Lines = nil
	for i := 0; i < 80; i++ {
		d.Lines = append(d.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", Quantity: 1, UnitPrice: 10, LineTotal: 10})
	}
	out, err := Render(d)
	require.NoError(t, err)

	pages := len(pageObjRE.FindAll(out, -1))
	require.GreaterOrEqual(t, pages, 3, "need enough pages to prove a repeat, not just a break")

	headerHits := strings.Count(pdfText(out), "(ITEM)")
	assert.GreaterOrEqual(t, headerHits, pages, "the header must be drawn again on every continuation page, not just the first")
}

func TestRender_FooterOnEveryPage(t *testing.T) {
	d := sampleDoc()
	d.Lines = nil
	for i := 0; i < 80; i++ {
		d.Lines = append(d.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", Quantity: 1, UnitPrice: 10, LineTotal: 10})
	}
	out, err := Render(d)
	require.NoError(t, err)
	text := pdfText(out)

	pages := len(pageObjRE.FindAll(out, -1))
	require.GreaterOrEqual(t, pages, 3)

	label := d.Kind + " " + d.Number
	assert.GreaterOrEqual(t, strings.Count(text, "("+label+")"), pages, "the document label prints in the footer of every page")
	for p := 1; p <= pages; p++ {
		want := fmt.Sprintf("(Page %d of %d)", p, pages)
		assert.Contains(t, text, want, "footer must show the right page number out of the right total on page %d", p)
	}
}

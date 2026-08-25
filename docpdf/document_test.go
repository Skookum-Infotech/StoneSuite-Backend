package docpdf

import (
	"bytes"
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

func TestRender(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PrintableDoc)
	}{
		{"basic", func(*PrintableDoc) {}},
		{"no lines", func(d *PrintableDoc) { d.Lines = nil }},
		{"with balance", func(d *PrintableDoc) { d.ShowBalance = true; d.AmountPaid = 100; d.BalanceDue = 711.88 }},
		{"multipage", func(d *PrintableDoc) {
			d.Lines = nil
			for i := 0; i < 80; i++ {
				d.Lines = append(d.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", UnitCode: "ea", Quantity: 1, UnitPrice: 10, LineTotal: 10})
			}
		}},
		{"full seller and both addresses", func(d *PrintableDoc) {
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
			tc.mutate(&d)
			out, err := Render(d)
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(out, []byte("%PDF-")), "output must be a PDF")
			assert.Greater(t, len(out), 500)
		})
	}
}

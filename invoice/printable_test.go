package invoice

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_Invoice(t *testing.T) {
	inv := Invoice{
		Number: "INV-1001", StatusName: "Sent", InvoiceDate: "2026-08-24", DueDate: "2026-09-23",
		Customer: CustomerRef{Name: "Bob Buyer"},
		Billing:  Address{CustomerName: "Bob Buyer", AddrLine1: "22 Main", City: "Dallas", Zip: "75201", Email: "bob@buyer.example"},
		Subtotal: 750, TaxTotal: 61.88, GrandTotal: 811.88, AmountPaid: 100, BalanceDue: 711.88,
		TermsConditions: "Net 30",
		Items:           []Line{{ItemName: "Granite Slab", SKU: "SLAB-1", Quantity: 3, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 750, UnitCode: "ea"}},
	}
	d := ToPrintable(inv, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "INVOICE", d.Kind)
	assert.Equal(t, "INV-1001", d.Number)
	assert.True(t, d.ShowBalance)
	assert.Equal(t, 711.88, d.BalanceDue)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "SLAB-1", d.Lines[0].SKU)

	email, name := Recipient(inv)
	assert.Equal(t, "bob@buyer.example", email)
	assert.Equal(t, "Bob Buyer", name)
}

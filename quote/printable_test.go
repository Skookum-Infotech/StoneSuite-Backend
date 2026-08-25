package quote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_Quote(t *testing.T) {
	q := Quote{
		Number: "QT-1001", Status: "Sent", QuoteDate: "2026-08-24", ValidUntil: "2026-09-23",
		Customer: CustomerRef{Name: "Bob Buyer"},
		Billing:  AddressInput{CustomerName: "Bob Buyer", AddrLine1: "22 Main", City: "Dallas", Zip: "75201", Email: "bob@buyer.example"},
		Subtotal: 750, TaxTotal: 61.88, GrandTotal: 811.88,
		TermsConditions: "Net 30",
		Items:           []Line{{ItemName: "Granite Slab", SKU: "SLAB-1", Quantity: 3, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 750, UnitCode: "ea"}},
	}
	d := ToPrintable(q, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "QUOTE", d.Kind)
	assert.Equal(t, "QT-1001", d.Number)
	assert.False(t, d.ShowBalance)
	assert.Equal(t, "2026-09-23", d.DueDate)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "SLAB-1", d.Lines[0].SKU)

	email, name := Recipient(q)
	assert.Equal(t, "bob@buyer.example", email)
	assert.Equal(t, "Bob Buyer", name)
}

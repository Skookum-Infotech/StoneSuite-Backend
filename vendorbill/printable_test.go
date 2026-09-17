package vendorbill

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_VendorBill(t *testing.T) {
	vb := VendorBill{
		Number: "VB-1001", StatusName: "Approved", BillDate: "2026-08-24", DueDate: "2026-09-23",
		Vendor:   VendorRef{ID: "v-1", Name: "Acme Supply"},
		Subtotal: 500, TaxTotal: 41.25, GrandTotal: 541.25,
		TermsConditions: "Net 30",
		Items:           []Line{{ItemName: "Granite Slab", SKU: "SLAB-1", Quantity: 2, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 500, UnitCode: "ea"}},
	}
	d := ToPrintable(vb, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "VENDOR BILL", d.Kind)
	assert.Equal(t, "VB-1001", d.Number)
	assert.Equal(t, "2026-09-23", d.DueDate)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "SLAB-1", d.Lines[0].SKU)
	assert.Equal(t, 541.25, d.GrandTotal)
}

func TestRecipient_VendorBill_NoVendor(t *testing.T) {
	vb := VendorBill{Vendor: VendorRef{}}
	email, name := Recipient(nil, nil, vb)
	assert.Empty(t, email)
	assert.Empty(t, name)
}

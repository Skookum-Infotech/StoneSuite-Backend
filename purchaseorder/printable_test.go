package purchaseorder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_PurchaseOrder(t *testing.T) {
	po := PurchaseOrder{
		Number: "PO-1001", Status: "Approved", OrderDate: "2026-08-24", ExpectedDate: "2026-09-01",
		Vendor:   VendorRef{ID: "v-1", Name: "Acme Supply"},
		ShipTo:   AddressInput{Name: "Warehouse", AddrLine1: "1 Dock Rd", City: "Dallas", Zip: "75201"},
		Subtotal: 500, TaxTotal: 41.25, GrandTotal: 541.25,
		TermsConditions: "Net 30",
		Items:           []Line{{ItemName: "Granite Slab", SKU: "SLAB-1", Quantity: 2, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 500, UnitCode: "ea"}},
	}
	d := ToPrintable(po, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "PURCHASE ORDER", d.Kind)
	assert.Equal(t, "PO-1001", d.Number)
	assert.Equal(t, "2026-09-01", d.DueDate)
	assert.Equal(t, "Warehouse", d.ShipTo.Name)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "SLAB-1", d.Lines[0].SKU)
	assert.Equal(t, 541.25, d.GrandTotal)
}

func TestRecipient_PurchaseOrder_NoVendor(t *testing.T) {
	// Vendor lookup requires a DB pool (covered by the dbtest suite); the
	// no-vendor short-circuit is pure and can be tested here.
	po := PurchaseOrder{Vendor: VendorRef{}}
	email, name := Recipient(nil, nil, po)
	assert.Empty(t, email)
	assert.Empty(t, name)
}

package vendorpayment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_VendorPayment(t *testing.T) {
	vp := VendorPayment{
		Number: "VP-1001", StatusName: "Paid",
		Vendor:      VendorRef{ID: "v-1", Name: "Acme Supply"},
		Memo:        "Payment for VB-1001",
		PaymentDate: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		Amount:      541.25,
	}
	d := ToPrintable(vp, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "VENDOR PAYMENT", d.Kind)
	assert.Equal(t, "VP-1001", d.Number)
	assert.Equal(t, "2026-08-24", d.IssueDate)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "Payment for VB-1001", d.Lines[0].Description)
	assert.Equal(t, 541.25, d.Lines[0].LineTotal)
	assert.Equal(t, 541.25, d.GrandTotal)
}

func TestRecipient_VendorPayment_NoVendor(t *testing.T) {
	vp := VendorPayment{Vendor: VendorRef{}}
	email, name := Recipient(context.Background(), nil, vp)
	assert.Empty(t, email)
	assert.Empty(t, name)
}

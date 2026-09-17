package vendorcredit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_VendorCredit(t *testing.T) {
	vc := VendorCredit{
		Number: "VC-1001", StatusName: "Open",
		Vendor:     VendorRef{ID: "v-1", Name: "Acme Supply"},
		Reason:     "Returned defective slab",
		Memo:       "See RMA-42",
		CreditDate: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		GrandTotal: 150,
	}
	d := ToPrintable(vc, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "VENDOR CREDIT", d.Kind)
	assert.Equal(t, "VC-1001", d.Number)
	assert.Equal(t, "2026-08-24", d.IssueDate)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "Returned defective slab", d.Lines[0].Description)
	assert.Equal(t, 150.0, d.Lines[0].LineTotal)
	assert.Equal(t, 150.0, d.GrandTotal)
}

func TestRecipient_VendorCredit_NoVendor(t *testing.T) {
	vc := VendorCredit{Vendor: VendorRef{}}
	email, name := Recipient(context.Background(), nil, vc)
	assert.Empty(t, email)
	assert.Empty(t, name)
}

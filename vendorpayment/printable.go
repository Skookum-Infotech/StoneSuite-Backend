package vendorpayment

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/vendors"
)

// ToPrintable maps a vendor payment to the renderer's PrintableDoc. A vendor
// payment is header-only (no line items) -- one synthetic PrintLine
// summarizes the payment amount and memo so the shared docpdf renderer can
// still be reused as-is.
func ToPrintable(vp VendorPayment, seller docpdf.Seller) docpdf.PrintableDoc {
	line := docpdf.PrintLine{
		Name: "Vendor Payment", Description: vp.Memo,
		Quantity: 1, UnitPrice: vp.Amount, LineTotal: vp.Amount,
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "VENDOR PAYMENT", Number: vp.Number, Status: vp.StatusName,
		IssueDate:  vp.PaymentDate.Format("2006-01-02"),
		Lines:      []docpdf.PrintLine{line},
		GrandTotal: vp.Amount,
		Notes:      vp.Memo,
	}
}

// Recipient resolves the default "Send to Vendor" email recipient the same
// way purchaseorder.Recipient does -- VendorRef carries no email snapshot, so
// this looks the vendor up.
func Recipient(ctx context.Context, pool *pgxpool.Pool, vp VendorPayment) (email, name string) {
	if vp.Vendor.ID == "" {
		return "", vp.Vendor.Name
	}
	v, err := vendors.Get(ctx, pool, vp.Vendor.ID)
	if err != nil || v == nil {
		return "", vp.Vendor.Name
	}
	email, name = vendors.ContactEmail(v)
	if name == "" {
		name = vp.Vendor.Name
	}
	return email, name
}

package vendorcredit

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/vendors"
)

// ToPrintable maps a vendor credit to the renderer's PrintableDoc. A vendor
// credit is header-only (no line items, AD-1) -- one synthetic PrintLine
// summarizes the credit amount and reason so the shared docpdf renderer can
// still be reused as-is.
func ToPrintable(vc VendorCredit, seller docpdf.Seller) docpdf.PrintableDoc {
	line := docpdf.PrintLine{
		Name: "Vendor Credit", Description: vc.Reason,
		Quantity: 1, UnitPrice: vc.GrandTotal, LineTotal: vc.GrandTotal,
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "VENDOR CREDIT", Number: vc.Number, Status: vc.StatusName,
		IssueDate: vc.CreditDate.Format("2006-01-02"),
		Lines:      []docpdf.PrintLine{line},
		GrandTotal: vc.GrandTotal,
		Notes:      vc.Memo,
	}
}

// Recipient resolves the default "Send to Vendor" email recipient the same
// way purchaseorder.Recipient does -- VendorRef carries no email snapshot, so
// this looks the vendor up.
func Recipient(ctx context.Context, pool *pgxpool.Pool, vc VendorCredit) (email, name string) {
	if vc.Vendor.ID == "" {
		return "", vc.Vendor.Name
	}
	v, err := vendors.Get(ctx, pool, vc.Vendor.ID)
	if err != nil || v == nil {
		return "", vc.Vendor.Name
	}
	email, name = vendors.ContactEmail(v)
	if name == "" {
		name = vc.Vendor.Name
	}
	return email, name
}

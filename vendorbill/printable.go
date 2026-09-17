package vendorbill

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/vendors"
)

// ToPrintable maps a vendor bill to the renderer's PrintableDoc. There is no
// bill-to block -- the tenant itself is the buyer -- so BillTo/ShipTo are
// left zero-valued, mirroring purchaseorder's buyer-is-the-tenant shape
// (vendor bills don't carry a ship-to address at all).
func ToPrintable(vb VendorBill, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(vb.Items))
	for _, it := range vb.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "VENDOR BILL", Number: vb.Number, Status: vb.StatusName,
		IssueDate: vb.BillDate, DueDate: vb.DueDate,
		Lines:    lines,
		Subtotal: vb.Subtotal, DiscountTotal: vb.DiscountTotal, TaxTotal: vb.TaxTotal,
		Adjustment: vb.Adjustment, GrandTotal: vb.GrandTotal,
		Terms: vb.TermsConditions, Notes: vb.Notes, Memo: vb.Memo,
	}
}

// Recipient resolves the default "Send to Vendor" email recipient the same
// way purchaseorder.Recipient does -- VendorRef carries no email snapshot, so
// this looks the vendor up.
func Recipient(ctx context.Context, pool *pgxpool.Pool, vb VendorBill) (email, name string) {
	if vb.Vendor.ID == "" {
		return "", vb.Vendor.Name
	}
	v, err := vendors.Get(ctx, pool, vb.Vendor.ID)
	if err != nil || v == nil {
		return "", vb.Vendor.Name
	}
	email, name = vendors.ContactEmail(v)
	if name == "" {
		name = vb.Vendor.Name
	}
	return email, name
}

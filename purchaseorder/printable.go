package purchaseorder

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/vendors"
)

// ToPrintable maps a purchase order to the renderer's PrintableDoc. There is
// no bill-to block -- the tenant itself is the buyer -- so only ShipTo is
// populated.
func ToPrintable(po PurchaseOrder, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(po.Items))
	for _, it := range po.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "PURCHASE ORDER", Number: po.Number, Status: po.Status,
		IssueDate: po.OrderDate, DueDate: po.ExpectedDate,
		ShipTo:   shipToPrintable(po.ShipTo),
		Lines:    lines,
		Subtotal: po.Subtotal, DiscountTotal: po.DiscountTotal, TaxTotal: po.TaxTotal,
		ShippingCharge: po.ShippingCharge, Adjustment: po.Adjustment, GrandTotal: po.GrandTotal,
		Terms: po.TermsConditions, Notes: po.Notes, Memo: po.Memo,
	}
}

// Recipient resolves the default "Send to Vendor" email recipient. Unlike the
// sales-side document modules, a purchase order only carries a light
// VendorRef{ID,Name,Number} -- no email snapshot -- so this looks the vendor
// up to get one. Failure to resolve the vendor (deleted, lookup error) simply
// yields an empty email, matching the sales-side behavior of an unresolvable
// recipient (caller surfaces "recipient required").
func Recipient(ctx context.Context, pool *pgxpool.Pool, po PurchaseOrder) (email, name string) {
	if po.Vendor.ID == "" {
		return "", po.Vendor.Name
	}
	v, err := vendors.Get(ctx, pool, po.Vendor.ID)
	if err != nil || v == nil {
		return "", po.Vendor.Name
	}
	email, name = vendors.ContactEmail(v)
	if name == "" {
		name = po.Vendor.Name
	}
	return email, name
}

func shipToPrintable(a AddressInput) docpdf.Address {
	cityStateZip := a.City
	if a.Zip != "" {
		if cityStateZip != "" {
			cityStateZip += " "
		}
		cityStateZip += a.Zip
	}
	return docpdf.Address{
		Name: a.Name, Attention: a.Attention, Line1: a.AddrLine1, Line2: a.AddrLine2,
		CityStateZip: cityStateZip, Phone: a.Phone, Email: a.Email,
	}
}

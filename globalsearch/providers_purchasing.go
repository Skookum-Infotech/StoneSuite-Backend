package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/expense"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/query"
	"stonesuite-backend/requisition"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/vendorpayment"
	"stonesuite-backend/vendors"
)

var _ = addProvider(Provider{Key: "vendor", Resource: authz.ResourceVendor, Search: searchVendors})

func searchVendors(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := vendors.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, v := range page.Records {
		out[i] = Result{Type: "vendor", ID: v.ID, Number: v.Number, DisplayName: v.DisplayName, UpdatedAt: v.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "requisition", Resource: authz.ResourceRequisition, Search: searchRequisitions})

func searchRequisitions(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := requisition.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, r := range page.Records {
		subtitle := ""
		if r.Vendor != nil {
			subtitle = r.Vendor.Name
		}
		out[i] = Result{Type: "requisition", ID: r.ID, Number: r.Number, DisplayName: "Requisition " + r.Number, Subtitle: subtitle, UpdatedAt: r.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "purchase_order", Resource: authz.ResourcePurchaseOrder, Search: searchPurchaseOrders})

func searchPurchaseOrders(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := purchaseorder.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, po := range page.Records {
		out[i] = Result{Type: "purchase_order", ID: po.ID, Number: po.Number, DisplayName: "Purchase Order " + po.Number, Subtitle: po.Vendor.Name, UpdatedAt: po.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "item_receipt", Resource: authz.ResourceItemReceipt, Search: searchItemReceipts})

func searchItemReceipts(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := itemreceipt.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, ir := range page.Records {
		out[i] = Result{Type: "item_receipt", ID: ir.ID, Number: ir.Number, DisplayName: "Item Receipt " + ir.Number, Subtitle: ir.Vendor.Name, UpdatedAt: ir.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "vendor_bill", Resource: authz.ResourceVendorBill, Search: searchVendorBills})

func searchVendorBills(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := vendorbill.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, vb := range page.Records {
		out[i] = Result{Type: "vendor_bill", ID: vb.ID, Number: vb.Number, DisplayName: "Vendor Bill " + vb.Number, Subtitle: vb.Vendor.Name, UpdatedAt: vb.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "vendor_payment", Resource: authz.ResourceVendorPayment, Search: searchVendorPayments})

func searchVendorPayments(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := vendorpayment.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, vp := range page.Records {
		out[i] = Result{Type: "vendor_payment", ID: vp.ID, Number: vp.Number, DisplayName: "Vendor Payment " + vp.Number, Subtitle: vp.Vendor.Name, UpdatedAt: vp.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "vendor_credit", Resource: authz.ResourceVendorCredit, Search: searchVendorCredits})

func searchVendorCredits(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := vendorcredit.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, vc := range page.Records {
		out[i] = Result{Type: "vendor_credit", ID: vc.ID, Number: vc.Number, DisplayName: "Vendor Credit " + vc.Number, Subtitle: vc.Vendor.Name, UpdatedAt: vc.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "expense", Resource: authz.ResourceExpense, Search: searchExpenses})

func searchExpenses(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := expense.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, ex := range page.Records {
		out[i] = Result{Type: "expense", ID: ex.ID, Number: ex.Number, DisplayName: "Expense " + ex.Number, Subtitle: ex.Department, UpdatedAt: ex.UpdatedAt}
	}
	return out, page.HasMore, nil
}

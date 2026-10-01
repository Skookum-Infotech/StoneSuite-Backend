package mytransactions

import "stonesuite-backend/authz"

// purchasingSources lists the vendor-facing purchasing modules. Their frontend
// route segment equals the key (purchases/<key>).
func purchasingSources() []Source {
	// A vendor's title is its legal name, falling back to the person's given +
	// family name (the same rule as vendors.displayName for individual vendors).
	vendor := doc("vendor", "Vendor", authz.ResourceVendor, "purchases", "vendor", "vendor", "vendor")
	vendor.Name = "COALESCE(NULLIF(t.vendor_legal_name, ''), NULLIF(TRIM(t.vendor_given_name || ' ' || t.vendor_family_name), ''))"

	// A requisition's own-scope column is the requester, not an owner.
	requisition := purchaseDoc("requisition", "Requisition", authz.ResourceRequisition, "requisition", "requisition", "requisition_estimated_total")
	requisition.Owner = "requisition_requested_by_id"

	// An expense has no vendor, and its own-scope column is the claimant.
	expense := doc("expense", "Expense", authz.ResourceExpense, "purchases", "expense", "expense", "expense")
	expense.Amount = "t.expense_total"
	expense.Owner = "expense_claimant_id"

	return []Source{
		vendor,
		requisition,
		purchaseDoc("purchase_order", "Purchase Order", authz.ResourcePurchaseOrder, "purchase_order", "purchase_order", "purchase_order_grand_total"),
		purchaseDoc("item_receipt", "Item Receipt", authz.ResourceItemReceipt, "item_receipt", "item_receipt", ""),
		purchaseDoc("vendor_bill", "Vendor Bill", authz.ResourceVendorBill, "vendor_bill", "vendor_bill", "vendor_bill_grand_total"),
		purchaseDoc("vendor_payment", "Vendor Payment", authz.ResourceVendorPayment, "vendor_payment", "vendor_payment", "vendor_payment_amount"),
		purchaseDoc("vendor_credit", "Vendor Credit", authz.ResourceVendorCredit, "vendor_credit", "vendor_credit", "vendor_credit_grand_total"),
		expense,
	}
}

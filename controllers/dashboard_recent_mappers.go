package controllers

import (
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/estimate"
	"stonesuite-backend/expense"
	"stonesuite-backend/invoice"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/payment"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/quote"
	"stonesuite-backend/refund"
	"stonesuite-backend/requisition"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/vendorpayment"
	"stonesuite-backend/workflow"
)

// mapRecent applies mapper to every record in records -- each recentSource's
// fetch closure (see dashboard_recent_sources.go) uses this to turn its
// module's own typed Page.Records into the widget's shared recentRecord
// shape.
func mapRecent[T any](records []T, mapper func(T) recentRecord) []recentRecord {
	out := make([]recentRecord, len(records))
	for i, rec := range records {
		out[i] = mapper(rec)
	}
	return out
}

// coreFieldString safely reads a string field out of a CRM record's
// CoreFields map, defaulting to "" for a missing key or an unexpected type
// rather than panicking -- CoreFields is a map[string]any assembled per CRM
// stage (see crmstore's scanRecord), not a typed struct.
func coreFieldString(core map[string]any, key string) string {
	s, _ := core[key].(string)
	return s
}

func strPtr(s string) *string     { return &s }
func floatPtr(f float64) *float64 { return &f }

// crmRecordToRecent maps a CRM (lead/prospect/customer) workflow.Record --
// the same shape across all three stages -- into the widget's shared row.
// Value is always nil: none of the three CRM stages carry a single
// monetary total the way a document module's GrandTotal/Amount does.
func crmRecordToRecent(rec workflow.Record) recentRecord {
	account := coreFieldString(rec.CoreFields, "customer_name")
	return recentRecord{
		ID: rec.ID, RecordNumber: rec.RecordNumber, Account: &account,
		Status: coreFieldString(rec.CoreFields, "crm_status_name"), UpdatedAt: rec.UpdatedAt,
	}
}

func quoteToRecent(q quote.Quote) recentRecord {
	return recentRecord{
		ID: q.ID, RecordNumber: q.Number, Account: strPtr(q.Customer.Name),
		Value: floatPtr(q.GrandTotal), Status: q.Status, UpdatedAt: q.UpdatedAt,
	}
}

func estimateToRecent(e estimate.Estimate) recentRecord {
	return recentRecord{
		ID: e.ID, RecordNumber: e.Number, Account: strPtr(e.Customer.Name),
		Value: floatPtr(e.GrandTotal), Status: e.Status, UpdatedAt: e.UpdatedAt,
	}
}

func salesOrderToRecent(o salesorder.Order) recentRecord {
	return recentRecord{
		ID: o.ID, RecordNumber: o.Number, Account: strPtr(o.Customer.Name),
		Value: floatPtr(o.GrandTotal), Status: o.Status, UpdatedAt: o.UpdatedAt,
	}
}

func invoiceToRecent(inv invoice.Invoice) recentRecord {
	return recentRecord{
		ID: inv.ID, RecordNumber: inv.Number, Account: strPtr(inv.Customer.Name),
		Value: floatPtr(inv.GrandTotal), Status: inv.StatusName, UpdatedAt: inv.UpdatedAt,
	}
}

func paymentToRecent(p payment.Payment) recentRecord {
	return recentRecord{
		ID: p.ID, RecordNumber: p.Number, Account: strPtr(p.Customer.Name),
		Value: floatPtr(p.Amount), Status: p.StatusName, UpdatedAt: p.UpdatedAt,
	}
}

func creditMemoToRecent(cm creditmemo.CreditMemo) recentRecord {
	return recentRecord{
		ID: cm.ID, RecordNumber: cm.Number, Account: strPtr(cm.Customer.Name),
		Value: floatPtr(cm.GrandTotal), Status: cm.StatusName, UpdatedAt: cm.UpdatedAt,
	}
}

func refundToRecent(rf refund.Refund) recentRecord {
	return recentRecord{
		ID: rf.ID, RecordNumber: rf.Number, Account: strPtr(rf.Customer.Name),
		Value: floatPtr(rf.Amount), Status: rf.StatusName, UpdatedAt: rf.UpdatedAt,
	}
}

// requisitionToRecent's Vendor is a nullable pointer -- a requisition may
// have no vendor picked yet, unlike every other purchases module -- so a nil
// Vendor maps to a nil Account rather than a zero-value dereference panic.
func requisitionToRecent(req requisition.Requisition) recentRecord {
	var account *string
	if req.Vendor != nil {
		account = strPtr(req.Vendor.Name)
	}
	return recentRecord{
		ID: req.ID, RecordNumber: req.Number, Account: account,
		Value: floatPtr(req.EstimatedTotal), Status: req.Status, UpdatedAt: req.UpdatedAt,
	}
}

func purchaseOrderToRecent(po purchaseorder.PurchaseOrder) recentRecord {
	return recentRecord{
		ID: po.ID, RecordNumber: po.Number, Account: strPtr(po.Vendor.Name),
		Value: floatPtr(po.GrandTotal), Status: po.Status, UpdatedAt: po.UpdatedAt,
	}
}

// itemReceiptToRecent has no Value: a receiving document carries no
// monetary total of its own.
func itemReceiptToRecent(ir itemreceipt.ItemReceipt) recentRecord {
	return recentRecord{
		ID: ir.ID, RecordNumber: ir.Number, Account: strPtr(ir.Vendor.Name),
		Status: ir.Status, UpdatedAt: ir.UpdatedAt,
	}
}

func vendorBillToRecent(vb vendorbill.VendorBill) recentRecord {
	return recentRecord{
		ID: vb.ID, RecordNumber: vb.Number, Account: strPtr(vb.Vendor.Name),
		Value: floatPtr(vb.GrandTotal), Status: vb.StatusName, UpdatedAt: vb.UpdatedAt,
	}
}

func vendorPaymentToRecent(vp vendorpayment.VendorPayment) recentRecord {
	return recentRecord{
		ID: vp.ID, RecordNumber: vp.Number, Account: strPtr(vp.Vendor.Name),
		Value: floatPtr(vp.Amount), Status: vp.StatusName, UpdatedAt: vp.UpdatedAt,
	}
}

func vendorCreditToRecent(vc vendorcredit.VendorCredit) recentRecord {
	return recentRecord{
		ID: vc.ID, RecordNumber: vc.Number, Account: strPtr(vc.Vendor.Name),
		Value: floatPtr(vc.GrandTotal), Status: vc.StatusName, UpdatedAt: vc.UpdatedAt,
	}
}

// expenseToRecent has no Account: an expense claim has a claimant employee,
// not a customer or vendor.
func expenseToRecent(ex expense.Expense) recentRecord {
	return recentRecord{
		ID: ex.ID, RecordNumber: ex.Number,
		Value: floatPtr(ex.Total), Status: ex.Status, UpdatedAt: ex.UpdatedAt,
	}
}

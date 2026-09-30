package globalsearch

import (
	"context"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/expense"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/requisition"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/vendorpayment"
	"stonesuite-backend/vendors"
)

// purchasingDateLayout renders time.Time document dates as yyyy-mm-dd.
const purchasingDateLayout = "2006-01-02"

// purchasingCore drops empty string values and zero-valued optional facts so a
// purchasing document stays small.
func purchasingCore(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// purchasingDate formats a non-zero time as yyyy-mm-dd, else "".
func purchasingDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(purchasingDateLayout)
}

var _ = addAI("vendor", AIHooks{
	OwnScoped: true,
	Label:     "vendor",
	Nouns:     []string{"supplier"},
	Load:      loadVendorAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "vendor", "vendor_deleted_at", "vendor_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "vendor", "vendor_uuid", "vendor_updated_at", "vendor_deleted_at")
	},
})

func loadVendorAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	v, err := vendors.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":      v.Number,
		"status":      v.Status,
		"vendor":      v.DisplayName,
		"vendor_type": v.VendorType,
		"legal_name":  v.LegalName,
		"email":       v.Email,
		"job_title":   v.JobTitle,
		"department":  v.Department,
	}
	if v.ContactPoint != nil {
		core["contact_email"] = v.ContactPoint.Email
		core["contact_phone"] = v.ContactPoint.Telephone
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "vendor", StateName: v.Status, Core: purchasingCore(core), Priority: []string{"number", "status", "vendor", "vendor_type", "email"}},
		OwnerUserID: v.OwnerUserID,
	}, nil
}

var _ = addAI("requisition", AIHooks{
	OwnScoped: true,
	Label:     "requisition",
	Nouns:     []string{"purchase requisition", "purchase request"},
	Load:      loadRequisitionAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "requisition", "requisition_deleted_at", "requisition_requested_by_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "requisition", "requisition_uuid", "requisition_updated_at", "requisition_deleted_at")
	},
})

func loadRequisitionAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	r, err := requisition.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(r.Items))
	for i, it := range r.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.EstimatedUnitPrice, Total: it.EstimatedAmount}
	}
	core := map[string]any{
		"number":      r.Number,
		"status":      r.Status,
		"approval":    r.ApprovalStatus,
		"date":        r.NeededByDate,
		"department":  r.Department,
		"priority":    r.Priority,
		"memo":        r.Memo,
		"subtotal":    r.Subtotal,
		"tax_total":   r.TaxTotal,
		"grand_total": r.EstimatedTotal,
		"line_items":  summarizeLines(lines),
	}
	if r.Vendor != nil {
		core["vendor"] = r.Vendor.Name
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "requisition", StateName: r.Status, Core: purchasingCore(core), Priority: aiPriorityDocument},
		OwnerUserID: r.OwnerUserID,
	}, nil
}

var _ = addAI("purchase_order", AIHooks{
	OwnScoped: true,
	Label:     "purchase order",
	Nouns:     []string{"purchase"},
	Load:      loadPurchaseOrderAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "purchase_order", "purchase_order_deleted_at", "purchase_order_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "purchase_order", "purchase_order_uuid", "purchase_order_updated_at", "purchase_order_deleted_at")
	},
})

func loadPurchaseOrderAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	po, err := purchaseorder.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(po.Items))
	for i, it := range po.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":        po.Number,
		"status":        po.Status,
		"approval":      po.ApprovalStatus,
		"vendor":        po.Vendor.Name,
		"date":          po.OrderDate,
		"expected_date": po.ExpectedDate,
		"po_number":     po.ReferenceNumber,
		"memo":          po.Memo,
		"subtotal":      po.Subtotal,
		"tax_total":     po.TaxTotal,
		"grand_total":   po.GrandTotal,
		"line_items":    summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "purchase_order", StateName: po.Status, Core: purchasingCore(core), Priority: aiPriorityDocument},
		OwnerUserID: po.OwnerUserID,
	}, nil
}

var _ = addAI("item_receipt", AIHooks{
	OwnScoped: true,
	Label:     "item receipt",
	Nouns:     []string{"goods receipt", "receipt"},
	Load:      loadItemReceiptAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "item_receipt", "item_receipt_deleted_at", "item_receipt_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "item_receipt", "item_receipt_uuid", "item_receipt_updated_at", "item_receipt_deleted_at")
	},
})

func loadItemReceiptAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	ir, err := itemreceipt.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(ir.Items))
	for i, it := range ir.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.QtyReceived}
	}
	core := map[string]any{
		"number":          ir.Number,
		"status":          ir.Status,
		"vendor":          ir.Vendor.Name,
		"purchase_order":  ir.PurchaseOrder.Number,
		"date":            ir.ReceiptDate,
		"location":        ir.WarehouseName,
		"packing_slip":    ir.PackingSlip,
		"carrier":         ir.Carrier,
		"tracking_number": ir.TrackingNumber,
		"memo":            ir.Notes,
		"line_items":      summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "item_receipt", StateName: ir.Status, Core: purchasingCore(core), Priority: []string{"number", "status", "vendor", "purchase_order", "date"}},
		OwnerUserID: ir.OwnerUserID,
	}, nil
}

var _ = addAI("vendor_bill", AIHooks{
	OwnScoped: true,
	Label:     "vendor bill",
	Nouns:     []string{"supplier bill", "bill"},
	Load:      loadVendorBillAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "vendor_bill", "vendor_bill_deleted_at", "vendor_bill_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "vendor_bill", "vendor_bill_uuid", "vendor_bill_updated_at", "vendor_bill_deleted_at")
	},
})

func loadVendorBillAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	vb, err := vendorbill.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(vb.Items))
	for i, it := range vb.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":         vb.Number,
		"status":         vb.StatusName,
		"approval":       vb.ApprovalStatus,
		"vendor":         vb.Vendor.Name,
		"invoice_number": vb.VendorInvoiceNumber,
		"date":           vb.BillDate,
		"due_date":       vb.DueDate,
		"memo":           vb.Memo,
		"subtotal":       vb.Subtotal,
		"tax_total":      vb.TaxTotal,
		"grand_total":    vb.GrandTotal,
		"paid_total":     vb.AmountPaid,
		"balance":        vb.BalanceDue,
		"line_items":     summarizeLines(lines),
	}
	if vb.PurchaseOrder != nil {
		core["purchase_order"] = vb.PurchaseOrder.Number
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "vendor_bill", StateName: vb.StatusName, Core: purchasingCore(core), Priority: aiPriorityDocument},
		OwnerUserID: vb.OwnerUserID,
	}, nil
}

var _ = addAI("vendor_payment", AIHooks{
	OwnScoped: true,
	Label:     "vendor payment",
	Nouns:     []string{"supplier payment", "bill payment"},
	Load:      loadVendorPaymentAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "vendor_payment", "vendor_payment_deleted_at", "vendor_payment_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "vendor_payment", "vendor_payment_uuid", "vendor_payment_updated_at", "vendor_payment_deleted_at")
	},
})

func loadVendorPaymentAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	vp, err := vendorpayment.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":        vp.Number,
		"status":        vp.StatusName,
		"approval":      vp.ApprovalStatus,
		"vendor":        vp.Vendor.Name,
		"date":          purchasingDate(vp.PaymentDate),
		"method":        vp.MethodName,
		"reference":     vp.ReferenceNumber,
		"memo":          vp.Memo,
		"grand_total":   vp.Amount,
		"applied_total": vp.AppliedTotal,
		"unapplied":     vp.UnappliedAmount,
	}
	if vp.ScheduledDate != nil {
		core["scheduled_date"] = purchasingDate(*vp.ScheduledDate)
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "vendor_payment", StateName: vp.StatusName, Core: purchasingCore(core), Priority: []string{"number", "status", "vendor", "date", "grand_total", "unapplied"}},
		OwnerUserID: vp.OwnerUserID,
	}, nil
}

var _ = addAI("vendor_credit", AIHooks{
	OwnScoped: true,
	Label:     "vendor credit",
	Nouns:     []string{"supplier credit", "vendor credit memo"},
	Load:      loadVendorCreditAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "vendor_credit", "vendor_credit_deleted_at", "vendor_credit_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "vendor_credit", "vendor_credit_uuid", "vendor_credit_updated_at", "vendor_credit_deleted_at")
	},
})

func loadVendorCreditAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	vc, err := vendorcredit.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":        vc.Number,
		"status":        vc.StatusName,
		"vendor":        vc.Vendor.Name,
		"date":          purchasingDate(vc.CreditDate),
		"reference":     vc.ReferenceNumber,
		"reason":        vc.Reason,
		"memo":          vc.Memo,
		"grand_total":   vc.GrandTotal,
		"applied_total": vc.AppliedTotal,
		"balance":       vc.UnappliedAmount,
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "vendor_credit", StateName: vc.StatusName, Core: purchasingCore(core), Priority: aiPriorityDocument},
		OwnerUserID: vc.OwnerUserID,
	}, nil
}

var _ = addAI("expense", AIHooks{
	OwnScoped: true,
	Label:     "expense",
	Nouns:     []string{"expense report", "expense claim"},
	Load:      loadExpenseAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "expense", "expense_deleted_at", "expense_claimant_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "expense", "expense_uuid", "expense_updated_at", "expense_deleted_at")
	},
})

func loadExpenseAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	ex, err := expense.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(ex.Items))
	for i, it := range ex.Items {
		lines[i] = aiLine{Name: it.CategoryName + ": " + it.Description, Quantity: 1, UnitPrice: it.Amount, Total: it.Amount}
	}
	core := map[string]any{
		"number":           ex.Number,
		"status":           ex.Status,
		"approval":         ex.ApprovalStatus,
		"department":       ex.Department,
		"memo":             ex.Memo,
		"rejection_reason": ex.RejectionReason,
		"grand_total":      ex.Total,
		"line_items":       summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "expense", StateName: ex.Status, Core: purchasingCore(core), Priority: []string{"number", "status", "department", "grand_total"}},
		OwnerUserID: ex.OwnerUserID,
	}, nil
}

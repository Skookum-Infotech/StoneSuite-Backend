package globalsearch

import (
	"context"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/estimate"
	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
	"stonesuite-backend/quote"
	"stonesuite-backend/refund"
	"stonesuite-backend/salesorder"
)

// aiPriorityDocument is the shared Priority order for document modules: what a
// question most often names, rendered first under the byte budget.
var aiPriorityDocument = []string{"number", "status", "customer", "date", "grand_total", "balance"}

var _ = addAI("quote", AIHooks{
	OwnScoped: true,
	Label:     "quote",
	Load:      loadQuoteAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "quote", "quote_deleted_at", "quote_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "quote", "quote_uuid", "quote_updated_at", "quote_deleted_at")
	},
})

func loadQuoteAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	q, err := quote.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(q.Items))
	for i, it := range q.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":      q.Number,
		"status":      q.Status,
		"customer":    q.Customer.Name,
		"date":        q.QuoteDate,
		"valid_until": q.ValidUntil,
		"po_number":   q.PONumber,
		"memo":        q.Memo,
		"subtotal":    q.Subtotal,
		"tax_total":   q.TaxTotal,
		"grand_total": q.GrandTotal,
		"line_items":  summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "quote", StateName: q.Status, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: q.OwnerUserID,
	}, nil
}

var _ = addAI("estimate", AIHooks{
	OwnScoped: true,
	Label:     "estimate",
	Load:      loadEstimateAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "estimate", "estimate_deleted_at", "estimate_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "estimate", "estimate_uuid", "estimate_updated_at", "estimate_deleted_at")
	},
})

func loadEstimateAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	est, err := estimate.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(est.Items))
	for i, it := range est.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":      est.Number,
		"status":      est.Status,
		"customer":    est.Customer.Name,
		"date":        est.EstimateDate,
		"valid_until": est.ValidUntil,
		"po_number":   est.PONumber,
		"memo":        est.Memo,
		"subtotal":    est.Subtotal,
		"tax_total":   est.TaxTotal,
		"grand_total": est.GrandTotal,
		"line_items":  summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "estimate", StateName: est.Status, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: est.OwnerUserID,
	}, nil
}

var _ = addAI("sales_order", AIHooks{
	OwnScoped: true,
	Label:     "sales order",
	Load:      loadSalesOrderAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "sales_order", "sales_order_deleted_at", "sales_order_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "sales_order", "sales_order_uuid", "sales_order_updated_at", "sales_order_deleted_at")
	},
})

func loadSalesOrderAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	ord, err := salesorder.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(ord.Items))
	for i, it := range ord.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":      ord.Number,
		"status":      ord.Status,
		"customer":    ord.Customer.Name,
		"date":        ord.OrderDate,
		"due_date":    ord.PaymentDueDate,
		"po_number":   ord.PONumber,
		"memo":        ord.Memo,
		"subtotal":    ord.Subtotal,
		"tax_total":   ord.TaxTotal,
		"grand_total": ord.GrandTotal,
		"line_items":  summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "sales_order", StateName: ord.Status, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: ord.OwnerUserID,
	}, nil
}

var _ = addAI("invoice", AIHooks{
	OwnScoped: true,
	Label:     "invoice",
	Load:      loadInvoiceAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "invoice", "invoice_deleted_at", "invoice_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "invoice", "invoice_uuid", "invoice_updated_at", "invoice_deleted_at")
	},
	// Sum answers "total outstanding balance" questions with a real SQL SUM —
	// "outstanding" means a nonzero balance due, the same literal meaning as
	// the question, not a date-based "overdue" (which would also need a
	// due_date predicate this pass doesn't add).
	Sum: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (float64, int, error) {
		return sumTable(ctx, pool, scope, identityID, "invoice", "invoice_balance_due", "invoice_deleted_at", "invoice_owner_id", "invoice_balance_due > 0")
	},
})

func loadInvoiceAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	inv, err := invoice.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(inv.Items))
	for i, it := range inv.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":      inv.Number,
		"status":      inv.StatusName,
		"customer":    inv.Customer.Name,
		"date":        inv.InvoiceDate,
		"due_date":    inv.DueDate,
		"po_number":   inv.PONumber,
		"memo":        inv.Memo,
		"subtotal":    inv.Subtotal,
		"tax_total":   inv.TaxTotal,
		"grand_total": inv.GrandTotal,
		"balance":     inv.BalanceDue,
		"line_items":  summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "invoice", StateName: inv.StatusName, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: inv.OwnerUserID,
	}, nil
}

var _ = addAI("payment", AIHooks{
	OwnScoped: true,
	Label:     "payment",
	Load:      loadPaymentAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "payment", "payment_deleted_at", "payment_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "payment", "payment_uuid", "payment_updated_at", "payment_deleted_at")
	},
})

func loadPaymentAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	p, err := payment.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":    p.Number,
		"status":    p.StatusName,
		"customer":  p.Customer.Name,
		"date":      p.PaymentDate,
		"memo":      p.Memo,
		"amount":    p.Amount,
		"unapplied": p.UnappliedAmount,
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "payment", StateName: p.StatusName, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: p.OwnerUserID,
	}, nil
}

var _ = addAI("credit_memo", AIHooks{
	OwnScoped: true,
	Label:     "credit memo",
	Load:      loadCreditMemoAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "credit_memo", "credit_memo_deleted_at", "credit_memo_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "credit_memo", "credit_memo_uuid", "credit_memo_updated_at", "credit_memo_deleted_at")
	},
})

func loadCreditMemoAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	cm, err := creditmemo.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(cm.Lines))
	for i, it := range cm.Lines {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":      cm.Number,
		"status":      cm.StatusName,
		"customer":    cm.Customer.Name,
		"date":        cm.CreditMemoDate,
		"memo":        cm.Memo,
		"subtotal":    cm.Subtotal,
		"tax_total":   cm.TaxTotal,
		"grand_total": cm.GrandTotal,
		"balance":     cm.UnappliedAmount,
		"line_items":  summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "credit_memo", StateName: cm.StatusName, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: cm.OwnerUserID,
	}, nil
}

var _ = addAI("refund", AIHooks{
	OwnScoped: true,
	Label:     "refund",
	Load:      loadRefundAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "refund", "refund_deleted_at", "refund_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "refund", "refund_uuid", "refund_updated_at", "refund_deleted_at")
	},
})

func loadRefundAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	rf, err := refund.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":    rf.Number,
		"status":    rf.StatusName,
		"customer":  rf.Customer.Name,
		"date":      rf.RefundDate,
		"memo":      rf.Memo,
		"amount":    rf.Amount,
		"unapplied": rf.UnappliedAmount,
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "refund", StateName: rf.StatusName, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: rf.OwnerUserID,
	}, nil
}

package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/estimate"
	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
	"stonesuite-backend/query"
	"stonesuite-backend/quote"
	"stonesuite-backend/refund"
	"stonesuite-backend/salesorder"
)

var _ = addProvider(Provider{Key: "quote", Resource: authz.ResourceQuote, Search: searchQuotes})

func searchQuotes(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := quote.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, q := range page.Records {
		out[i] = Result{Type: "quote", ID: q.ID, Number: q.Number, DisplayName: "Quote " + q.Number, Subtitle: q.Customer.Name, UpdatedAt: q.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "estimate", Resource: authz.ResourceEstimate, Search: searchEstimates})

func searchEstimates(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := estimate.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, e := range page.Records {
		out[i] = Result{Type: "estimate", ID: e.ID, Number: e.Number, DisplayName: "Estimate " + e.Number, Subtitle: e.Customer.Name, UpdatedAt: e.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "sales_order", Resource: authz.ResourceSalesOrder, Search: searchSalesOrders})

func searchSalesOrders(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := salesorder.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, o := range page.Records {
		out[i] = Result{Type: "sales_order", ID: o.ID, Number: o.Number, DisplayName: "Sales Order " + o.Number, Subtitle: o.Customer.Name, UpdatedAt: o.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "invoice", Resource: authz.ResourceInvoice, Search: searchInvoices})

func searchInvoices(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := invoice.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, inv := range page.Records {
		out[i] = Result{Type: "invoice", ID: inv.ID, Number: inv.Number, DisplayName: "Invoice " + inv.Number, Subtitle: inv.Customer.Name, UpdatedAt: inv.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "payment", Resource: authz.ResourcePayment, Search: searchPayments})

func searchPayments(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := payment.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, p := range page.Records {
		out[i] = Result{Type: "payment", ID: p.ID, Number: p.Number, DisplayName: "Payment " + p.Number, Subtitle: p.Customer.Name, UpdatedAt: p.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "credit_memo", Resource: authz.ResourceCreditMemo, Search: searchCreditMemos})

func searchCreditMemos(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := creditmemo.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, cm := range page.Records {
		out[i] = Result{Type: "credit_memo", ID: cm.ID, Number: cm.Number, DisplayName: "Credit Memo " + cm.Number, Subtitle: cm.Customer.Name, UpdatedAt: cm.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "refund", Resource: authz.ResourceRefund, Search: searchRefunds})

func searchRefunds(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := refund.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, rf := range page.Records {
		out[i] = Result{Type: "refund", ID: rf.ID, Number: rf.Number, DisplayName: "Refund " + rf.Number, Subtitle: rf.Customer.Name, UpdatedAt: rf.UpdatedAt}
	}
	return out, page.HasMore, nil
}

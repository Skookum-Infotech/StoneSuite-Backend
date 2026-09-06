package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/estimate"
	"stonesuite-backend/expense"
	"stonesuite-backend/invoice"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/payment"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/query"
	"stonesuite-backend/quote"
	"stonesuite-backend/refund"
	"stonesuite-backend/requisition"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/vendorpayment"
)

// buildRecentSources returns every module the Recent records widget can pull
// from, closed over the request-scoped ctx/pool/actorIdentityID/since/
// fetchLimit so each source's fetch only needs the caller's resolved scope
// for that module (see buildRecentRecords). Every module already exposes a
// Search(ctx, pool, scope, actorIdentityID, query.Request) -- no new SQL, no
// new registry -- and "updated_at" is one of the query engine's built-in
// sort/filter keys on every one of them (see query/builder.go's
// sortableFields), so one shared req serves all 17 sources. Order in the
// returned slice doesn't matter: mergeRecentRecords re-sorts everything by
// updated_at.
func buildRecentSources(ctx context.Context, st crmstore.Store, pool *pgxpool.Pool, actorIdentityID string, since time.Time, fetchLimit int) []recentSource {
	req := query.Request{
		Sort:    []query.SortKey{{Field: "updated_at", Dir: query.DirDesc}},
		Filters: updatedAtSinceFilter(since),
		Limit:   fetchLimit,
	}

	crmSource := func(key string, resource authz.Resource) recentSource {
		return recentSource{
			module: key, domain: "crm", resource: resource,
			fetch: func(scope authz.Scope) ([]recentRecord, error) {
				page, err := st.SearchRecords(ctx, pool, key, string(scope), actorIdentityID, req)
				if err != nil {
					return nil, fmt.Errorf("search %s: %w", key, err)
				}
				return mapRecent(page.Records, crmRecordToRecent), nil
			},
		}
	}

	return []recentSource{
		crmSource("lead", authz.ResourceLead),
		crmSource("prospect", authz.ResourceProspect),
		crmSource("customer", authz.ResourceCustomer),
		{module: "quote", domain: "sales", resource: authz.ResourceQuote, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := quote.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search quote: %w", err)
			}
			return mapRecent(page.Records, quoteToRecent), nil
		}},
		{module: "estimate", domain: "sales", resource: authz.ResourceEstimate, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := estimate.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search estimate: %w", err)
			}
			return mapRecent(page.Records, estimateToRecent), nil
		}},
		{module: "sales_order", domain: "sales", resource: authz.ResourceSalesOrder, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := salesorder.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search sales_order: %w", err)
			}
			return mapRecent(page.Records, salesOrderToRecent), nil
		}},
		{module: "invoice", domain: "sales", resource: authz.ResourceInvoice, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := invoice.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search invoice: %w", err)
			}
			return mapRecent(page.Records, invoiceToRecent), nil
		}},
		{module: "payment", domain: "sales", resource: authz.ResourcePayment, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := payment.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search payment: %w", err)
			}
			return mapRecent(page.Records, paymentToRecent), nil
		}},
		{module: "credit_memo", domain: "sales", resource: authz.ResourceCreditMemo, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := creditmemo.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search credit_memo: %w", err)
			}
			return mapRecent(page.Records, creditMemoToRecent), nil
		}},
		{module: "refund", domain: "sales", resource: authz.ResourceRefund, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := refund.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search refund: %w", err)
			}
			return mapRecent(page.Records, refundToRecent), nil
		}},
		{module: "requisition", domain: "purchases", resource: authz.ResourceRequisition, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := requisition.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search requisition: %w", err)
			}
			return mapRecent(page.Records, requisitionToRecent), nil
		}},
		{module: "purchase_order", domain: "purchases", resource: authz.ResourcePurchaseOrder, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := purchaseorder.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search purchase_order: %w", err)
			}
			return mapRecent(page.Records, purchaseOrderToRecent), nil
		}},
		{module: "item_receipt", domain: "purchases", resource: authz.ResourceItemReceipt, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := itemreceipt.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search item_receipt: %w", err)
			}
			return mapRecent(page.Records, itemReceiptToRecent), nil
		}},
		{module: "vendor_bill", domain: "purchases", resource: authz.ResourceVendorBill, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := vendorbill.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search vendor_bill: %w", err)
			}
			return mapRecent(page.Records, vendorBillToRecent), nil
		}},
		{module: "vendor_payment", domain: "purchases", resource: authz.ResourceVendorPayment, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := vendorpayment.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search vendor_payment: %w", err)
			}
			return mapRecent(page.Records, vendorPaymentToRecent), nil
		}},
		{module: "vendor_credit", domain: "purchases", resource: authz.ResourceVendorCredit, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := vendorcredit.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search vendor_credit: %w", err)
			}
			return mapRecent(page.Records, vendorCreditToRecent), nil
		}},
		{module: "expense", domain: "purchases", resource: authz.ResourceExpense, fetch: func(scope authz.Scope) ([]recentRecord, error) {
			page, err := expense.Search(ctx, pool, string(scope), actorIdentityID, req)
			if err != nil {
				return nil, fmt.Errorf("search expense: %w", err)
			}
			return mapRecent(page.Records, expenseToRecent), nil
		}},
	}
}

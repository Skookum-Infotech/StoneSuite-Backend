// Package doclinks resolves the end-to-end document chain a sales record belongs
// to (Quote, Sales Order, Fabrication, Invoice, Payment, Refund) so every detail
// page can return the same read-only `workflow` block. The chain is computed on
// read from foreign keys that already exist; nothing is stored.
//
// Resolution walks UP from the viewed record to its roots, then DOWN from every
// node found, so opening any record of a flow yields the same block.
package doclinks

import (
	"context"
	"fmt"
)

// Kind names a document type that can sit in the chain.
type Kind string

const (
	KindQuote       Kind = "quote"
	KindSalesOrder  Kind = "salesOrder"
	KindFabrication Kind = "fabrication"
	KindInvoice     Kind = "invoice"
	KindPayment     Kind = "payment"
	KindCreditMemo  Kind = "creditMemo" // bridge only: never a stage, links refunds to invoices
	KindRefund      Kind = "refund"
)

// maxChainNodes bounds the walk so a pathological tenant cannot make one detail
// request unbounded; a real flow has a handful of documents.
const maxChainNodes = 500

// maxChainRounds bounds the closure loops; the longest legitimate path
// (refund to credit memo to invoice to order to quote) is five hops.
const maxChainRounds = 12

// IDs is a set of internal numeric row ids of one kind.
type IDs map[int32]struct{}

// Chain is the set of internal ids found in a flow, by kind.
type Chain map[Kind]IDs

// edge is one foreign-key hop: the SQL takes the from-ids ($1) and returns to-ids.
type edge struct {
	from, to Kind
	sql      string
}

// Loader runs one edge hop. The SQL implementation is pgLoader; tests fake it.
type Loader interface {
	Hop(ctx context.Context, e edge, from []int32) ([]int32, error)
}

// upEdges lead from a child toward the root of the flow.
var upEdges = []edge{
	{KindRefund, KindPayment, `SELECT refund_payment_id FROM refund WHERE refund_id = ANY($1) AND refund_payment_id IS NOT NULL
		UNION SELECT payment_id FROM refund_application WHERE refund_id = ANY($1) AND payment_id IS NOT NULL AND application_deleted_at IS NULL`},
	{KindRefund, KindInvoice, `SELECT refund_invoice_id FROM refund WHERE refund_id = ANY($1) AND refund_invoice_id IS NOT NULL`},
	{KindRefund, KindCreditMemo, `SELECT refund_credit_memo_id FROM refund WHERE refund_id = ANY($1) AND refund_credit_memo_id IS NOT NULL
		UNION SELECT credit_memo_id FROM refund_application WHERE refund_id = ANY($1) AND credit_memo_id IS NOT NULL AND application_deleted_at IS NULL`},
	{KindCreditMemo, KindSalesOrder, `SELECT credit_memo_sales_order_id FROM credit_memo WHERE credit_memo_id = ANY($1) AND credit_memo_sales_order_id IS NOT NULL`},
	{KindCreditMemo, KindInvoice, `SELECT credit_memo_invoice_id FROM credit_memo WHERE credit_memo_id = ANY($1) AND credit_memo_invoice_id IS NOT NULL
		UNION SELECT invoice_id FROM credit_memo_application WHERE credit_memo_id = ANY($1) AND application_deleted_at IS NULL`},
	{KindCreditMemo, KindPayment, `SELECT credit_memo_source_payment_id FROM credit_memo WHERE credit_memo_id = ANY($1) AND credit_memo_source_payment_id IS NOT NULL`},
	{KindPayment, KindInvoice, `SELECT invoice_id FROM payment_application WHERE payment_id = ANY($1) AND application_deleted_at IS NULL`},
	{KindInvoice, KindSalesOrder, `SELECT invoice_sales_order_id FROM invoice WHERE invoice_id = ANY($1) AND invoice_sales_order_id IS NOT NULL`},
	{KindFabrication, KindSalesOrder, `SELECT sales_order_id FROM fabrication_job WHERE fabrication_job_id = ANY($1)`},
	{KindSalesOrder, KindQuote, `SELECT quote_id FROM quote_conversion WHERE sales_order_id = ANY($1)`},
}

// downEdges lead from a parent to the records raised from it.
var downEdges = []edge{
	{KindQuote, KindSalesOrder, `SELECT sales_order_id FROM quote_conversion WHERE quote_id = ANY($1)`},
	{KindSalesOrder, KindFabrication, `SELECT fabrication_job_id FROM fabrication_job WHERE sales_order_id = ANY($1) AND fabrication_job_deleted_at IS NULL`},
	{KindSalesOrder, KindInvoice, `SELECT invoice_id FROM invoice WHERE invoice_sales_order_id = ANY($1) AND invoice_deleted_at IS NULL`},
	{KindSalesOrder, KindCreditMemo, `SELECT credit_memo_id FROM credit_memo WHERE credit_memo_sales_order_id = ANY($1) AND credit_memo_deleted_at IS NULL`},
	{KindInvoice, KindPayment, `SELECT payment_id FROM payment_application WHERE invoice_id = ANY($1) AND application_deleted_at IS NULL`},
	{KindInvoice, KindCreditMemo, `SELECT credit_memo_id FROM credit_memo WHERE credit_memo_invoice_id = ANY($1) AND credit_memo_deleted_at IS NULL
		UNION SELECT credit_memo_id FROM credit_memo_application WHERE invoice_id = ANY($1) AND application_deleted_at IS NULL`},
	{KindInvoice, KindRefund, `SELECT refund_id FROM refund WHERE refund_invoice_id = ANY($1) AND refund_deleted_at IS NULL`},
	{KindPayment, KindRefund, `SELECT refund_id FROM refund WHERE refund_payment_id = ANY($1) AND refund_deleted_at IS NULL
		UNION SELECT refund_id FROM refund_application WHERE payment_id = ANY($1) AND application_deleted_at IS NULL`},
	{KindPayment, KindCreditMemo, `SELECT credit_memo_id FROM credit_memo WHERE credit_memo_source_payment_id = ANY($1) AND credit_memo_deleted_at IS NULL`},
	{KindCreditMemo, KindRefund, `SELECT refund_id FROM refund WHERE refund_credit_memo_id = ANY($1) AND refund_deleted_at IS NULL
		UNION SELECT refund_id FROM refund_application WHERE credit_memo_id = ANY($1) AND application_deleted_at IS NULL`},
}

// Walk returns every document in the flow of the viewed record: up to the roots,
// then down from every node reached. The viewed record is always included.
func Walk(ctx context.Context, l Loader, kind Kind, id int32) (Chain, error) {
	chain := Chain{kind: {id: {}}}
	if err := closure(ctx, l, chain, upEdges); err != nil {
		return nil, err
	}
	if err := closure(ctx, l, chain, downEdges); err != nil {
		return nil, err
	}
	return chain, nil
}

// closure applies edges repeatedly until no new node appears.
func closure(ctx context.Context, l Loader, chain Chain, edges []edge) error {
	for round := 0; round < maxChainRounds; round++ {
		added := false
		for _, e := range edges {
			from := chain.ids(e.from)
			if len(from) == 0 {
				continue
			}
			found, err := l.Hop(ctx, e, from)
			if err != nil {
				return fmt.Errorf("walk %s to %s: %w", e.from, e.to, err)
			}
			for _, n := range found {
				if chain.add(e.to, n) {
					added = true
				}
			}
			if chain.size() > maxChainNodes {
				return fmt.Errorf("document chain exceeds %d records", maxChainNodes)
			}
		}
		if !added {
			return nil
		}
	}
	return nil
}

func (c Chain) ids(k Kind) []int32 {
	out := make([]int32, 0, len(c[k]))
	for id := range c[k] {
		out = append(out, id)
	}
	return out
}

func (c Chain) add(k Kind, id int32) bool {
	if c[k] == nil {
		c[k] = IDs{}
	}
	if _, ok := c[k][id]; ok {
		return false
	}
	c[k][id] = struct{}{}
	return true
}

func (c Chain) size() int {
	n := 0
	for _, s := range c {
		n += len(s)
	}
	return n
}

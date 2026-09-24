# Sales Documents

StoneSuite's sales documents form a chain: an Estimate can become a Quote, a
Quote can become a Sales Order, and a Sales Order can become an Invoice.
Payments, Credit Memos, and Refunds apply against a customer's invoices.
Each document type lives under the Sales section of the sidebar.

## Estimates

An Estimate is a rough, early-stage price for a customer, created from
Sales → Estimates → New Estimate. It starts as Draft and moves through
Pending Approval, Approved, Sent, Rejected, Expired, or Cancelled. Only an
Active customer can be picked when creating one. An Approved or Sent estimate
can be converted into a Quote with the Convert to Quote button, which creates
a new quote from the estimate's details.

## Quotes

A Quote is a firmer, ready-to-send price, created from Sales → Quotes → New
Quote (or by converting an estimate). It uses the same statuses as an
estimate: Draft, Pending Approval, Approved, Sent, Rejected, Expired,
Cancelled. An Approved or Sent quote can be converted into a Sales Order with
the Convert to Sales Order button, which creates a new sales order from the
quote's details.

## Sales Orders

A Sales Order is the customer's confirmed order, created from Sales → Sales
Orders → New Sales Order (or by converting a quote). Its statuses are Draft,
Pending Approval, Approved, Open, Partially Filled, Filled, or Cancelled. A
sales order detail page also has an Inventory tab alongside its Overview,
Items, Audit, and Files tabs. Once Approved, Open, Partially Filled, or
Filled, it can be converted into an Invoice with the Convert to Invoice
button. A sales order is also the starting point for a fabrication job — see
`fabrication.md`.

## Invoices

An Invoice is created from Sales → Invoices → New Invoice, or by converting a
sales order. Its statuses are Draft, Pending Approval, Approved, Sent,
Partially Paid, Overdue, Paid, or Void. Payments and credit memos are applied
against an invoice to move it toward Paid; an invoice that's past its due
date without being fully paid shows as Overdue.

## Payments

A Payment records money received from a customer, created from Sales →
Payments → New Payment. When creating one, you can optionally apply it to one
or more of the customer's open invoices. Its statuses are Pending, Approved,
Deposited, or Void — Deposited means the money has been recorded as
received into your bank.

## Credit Memos

A Credit Memo records a credit issued to a customer — for a return, an
overcharge, or a goodwill adjustment — created from Sales → Credit Memos →
New Credit Memo. Its statuses are Draft, Approved, Applied, or Void; Applied
means the credit has been used against an invoice, the same way a Payment
is applied to reduce what a customer still owes.

## Refunds

A Refund records money paid back to a customer, created from Sales → Refunds
→ New Refund. You can optionally link it to related documents when creating
it, so the refund shows what it's connected to. Its statuses are Pending,
Approved, Sent, or Void.

## Approvals

Any of these document types can have approvers configured for its workflow
(Configure → Workflows). When approvers are set, a new or edited record waits
in a pending-approval status until every configured approver signs off before
it can move forward — for example, only an Approved (or Sent) quote offers
the Convert to Sales Order button, so approval is a checkpoint before a
document can move to the next stage. See `administration.md` for how
approval chains are set up.

## Sending a document by email

On a Quote, Estimate, Sales Order, or Invoice, use the Send to Customer
action in the Quick Actions panel to email the document as a PDF to the
customer's email on file. A confirmation dialog shows who it will be sent to
before it goes out, so you can double-check the recipient before the email
leaves StoneSuite.

## Exporting a document as a PDF

Every sales document — Estimate, Quote, Sales Order, Invoice, Payment,
Credit Memo, and Refund — has an Export PDF action in its Quick Actions
panel, which downloads a PDF copy of the document for you to save, print, or
send yourself outside of StoneSuite's built-in Send to Customer action.

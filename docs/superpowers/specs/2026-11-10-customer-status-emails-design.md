# Customer status emails across the order lifecycle — design

Date: 2026-11-10. Source: audit in `.claude/plans/uh-check-end-to-end-email-stateless-popcorn.md`.

## Problem
Customers are only emailed on final approval (sales order, invoice, payment, refund), manual document send, and AR dunning. Status moves (Open/Filled/Cancelled, fabrication stages, invoice Sent/Paid/Void, payment received, refund sent, quote Sent, credit memo approved/applied), deletes and portal order-form submit send the customer nothing. Fabrication Cancel also never notifies staff.

## Design
1. **One generic sender** `notifyCustomerEvent` in new `controllers/customer_status_notify.go`, generalising `notifyCustomerApproved` (`controllers/customer_notify.go`). Same guards, unchanged: tenant+pool from ctx, `customerOptedOut` (fail-closed Do Not Contact), portal invite -> `PortalLinkActive` -> identity recipient, `ActorUserID` set, `services.SendNotification`, errors logged via slog, never fails the HTTP response. `notifyCustomerApproved` becomes a thin wrapper (signature kept).
2. **Copy table** `customerStatusCopy(resource, statusCode) (statusCopy, bool)` — pure, table-driven, tested. Only listed (resource,status) pairs send; everything else is a no-op. EventType `<resource>.customer_status` (+ `.customer_deleted`, `.customer_received`).
3. **Hook point = controller, after the store call succeeds** (same place as `notifyCustomerApproved`). Stores stay untouched except the fabrication Cancel fix.
4. **Coverage (resource: statuses)**
   - sales_order: OPEN, PART, FILL, CANC; delete only if status was customer-visible (APPV+, per `portal/visibility.go`); order-form submit confirmation to the submitting customer.
   - quote: SENT; customer-approved notice.
   - invoice: SENT, PART, PAID, VOID, ODUE (PART/PAID also fire from payment apply).
   - payment: DEPO / applied ("payment received").
   - refund: SENT. credit memo: APPV, APPL.
   - fabrication: every stage change, HOLD, resume, CANC, customer template approval (needs `cp` injected into `FabricationOps`; job has `Customer`).
5. **Fix**: `fabrication/store_cancel.go` must call `notifyTransition` like other modules.
6. **Vendor modules** (vendorbill/vendorpayment/vendorcredit): audit only; report gaps, no vendor-facing work.

## Decisions needing approval
- **D1 Recipient scope.** Existing guard emails only customers with an active portal login. Default here: keep that (consistent, no new PII path). Alternative: fall back to the customer's contact email like AR dunning does. Many customers have no portal login, so default means they receive nothing.
- **D2 Noise.** Fabrication has ~16 stages; default emails every stage change. Alternative: customer-meaningful subset only.
- No hardcoded addresses anywhere; test recipients come from fixtures.

## Testing
Table tests for the copy table and request builder; guard tests (opted out, no invite, inactive link) via existing lookup seams; handler wiring checked by `module-drift-checker`.

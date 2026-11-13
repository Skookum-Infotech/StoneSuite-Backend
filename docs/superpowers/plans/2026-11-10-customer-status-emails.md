# Customer status emails — implementation plan

Spec: `docs/superpowers/specs/2026-11-10-customer-status-emails-design.md`. Reference: `controllers/customer_notify.go`.

## Step 1 (sequential) — generic sender + copy table
Create `controllers/customer_status_notify.go` and `_test.go`: `customerEvent`, `notifyCustomerEvent` (guards copied from `notifyCustomerApproved`), `buildCustomerEventNotification`, pure `customerStatusCopy(resource, status)`. Make `notifyCustomerApproved` delegate; keep signature. Files: those three + `customer_notify.go`.

## Step 2 (independent, after 1) — hand-wire per module, one dispatch each
Call `notifyCustomerStatus` after the success path, beside existing audit calls:
- 2a salesorder: `controllers/salesorder.go` Transition + delete; `salesorder_customer_form.go` submit confirmation; `salesorder_convert.go` if it creates.
- 2b quote: `controllers/quote.go` transition SENT + approve; `quote_convert.go` no change.
- 2c invoice: `invoice_transition.go` Transition; `payment_transition.go` Apply/Unapply (PART/PAID).
- 2d payment + refund + creditmemo: `payment_transition.go`, `refund_transition.go`, `creditmemo_transition.go`, `creditmemo.go` approve/apply.
- 2e fabrication: `FabricationOps` gets `cp` (`NewFabricationOps(cp)`, call site in `main.go`, only that line); `fabrication_transition.go`, `fabrication_customer_approval.go`.

## Step 3 (independent) — Cancel fix
`fabrication/store_cancel.go`: call `notifyTransition` exactly as `store_transition.go:137-145` does.

## Step 4 (sequential, last) — docs + vendor audit
Update the matching `docs/` help article. Vendor modules: grep and report only.

## Verify
`go build ./... && go vet ./... && go test ./...`; module-drift-checker + tenancy-security-reviewer on the diff.

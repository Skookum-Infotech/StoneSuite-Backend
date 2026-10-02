# Date-only JSON response fields — implementation plan

Spec: `docs/superpowers/specs/2026-10-02-date-only-json-fields-design.md`

Reference pattern for steps 1–6: `salesorder/types.go` (`OrderDate string // "yyyy-mm-dd"`)
and `salesorder/store.go` (`to_char(so.sales_order_date,'YYYY-MM-DD')`).
Request/input types are **not** changed in any step. No schema, route or RBAC changes.

Steps 1–6, 8, 9 are **independent** (disjoint packages/files). Step 7 is
**sequential** after 1–6 (it reads the new field types). Step 10 is sequential last.

---

### Step 1 — payment (independent)
Files: `payment/types.go`, `payment/store.go`, `payment/types_test.go`.
- `Payment.PaymentDate` → `string` with comment `// "yyyy-mm-dd"`. Do not touch input structs.
- In the SELECT in `payment/store.go`, replace `p.payment_date` with `to_char(p.payment_date,'YYYY-MM-DD')`.
- `search.go` `sortValue` keeps `return p.PaymentDate` (now a string; no edit needed).
- `types_test.go`: set `PaymentDate: "2026-01-02"`, drop the `time` import if unused, and assert the marshaled `paymentDate` equals `"2026-01-02"`.
- Grep the package (incl. `*_dbtest_test.go`) for other uses of `.PaymentDate` on the `Payment` struct and adapt.

### Step 2 — refund (independent)
Files: `refund/types.go`, `refund/store.go`, `refund/*_test.go` only if they reference the field.
- `Refund.RefundDate` → `string // "yyyy-mm-dd"`; SELECT `to_char(rfnd.refund_date,'YYYY-MM-DD')`.
- Add `refund/types_test.go` JSON-shape test asserting `refundDate` == `"2026-01-02"` (mirror `payment/types_test.go`).

### Step 3 — creditmemo (independent)
Files: `creditmemo/types.go`, `creditmemo/store.go`, new `creditmemo/types_test.go`.
- `CreditMemo.CreditMemoDate` → `string // "yyyy-mm-dd"`; SELECT `to_char(cm.credit_memo_date,'YYYY-MM-DD')`.
- New JSON-shape test asserting `creditMemoDate` == `"2026-01-02"`.

### Step 4 — vendorpayment (independent)
Files: `vendorpayment/types.go`, `vendorpayment/store.go`, `vendorpayment/printable.go`, `vendorpayment/printable_test.go`, new `vendorpayment/types_test.go`.
- `VendorPayment.PaymentDate` → `string`, `ScheduledDate` → `*string` (keep `omitempty`).
- SELECT: `to_char(vp.vendor_payment_date,'YYYY-MM-DD')`, `to_char(vp.vendor_payment_scheduled_date,'YYYY-MM-DD')` (no COALESCE — NULL scans to nil). Change the local `scheduledDate` var in the scan function to `*string`.
- `printable.go`: `IssueDate: vp.PaymentDate` (drop `.Format`); remove `time` import if unused.
- Tests: fixture `PaymentDate: "2026-08-24"`; JSON test asserting `paymentDate` == `"2026-08-24"`, `scheduledDate` present as `"2026-09-01"` when set and absent when nil.

### Step 5 — vendorcredit (independent)
Files: `vendorcredit/types.go`, `vendorcredit/store_read.go`, `vendorcredit/printable.go`, `vendorcredit/printable_test.go`, new `vendorcredit/types_test.go`.
- `VendorCredit.CreditDate` → `string // "yyyy-mm-dd"`; SELECT `to_char(vc.vendor_credit_date,'YYYY-MM-DD')`.
- `printable.go`: `IssueDate: vc.CreditDate`.
- Tests: fixture string; JSON test asserting `creditDate` == `"2026-08-24"`.

### Step 6 — cashtransfer (independent)
Files: `cashtransfer/types.go`, `cashtransfer/store.go`, `cashtransfer/store_search_test.go`, new `cashtransfer/types_test.go`.
- `CashTransfer.TransferDate` → `string // "yyyy-mm-dd"`; SELECT `to_char(ct.cash_transfer_date,'YYYY-MM-DD')`.
- Do **not** touch `recent.go`, `store_create.go`, `store_post.go` (they use their own `time.Time` locals/inputs).
- Fixture `TransferDate: "2026-03-04"`; JSON test asserting `transferDate` == `"2026-03-04"`.

### Step 7 — globalsearch AI core (sequential, after 1–6)
Files: `globalsearch/ai_sales.go`, `globalsearch/ai_purchasing.go`, `globalsearch/ai_inventory.go`, and their `_test.go` only if they reference these fields.
- `ai_sales.go`: `"date": p.PaymentDate` / `cm.CreditMemoDate` / `rf.RefundDate` — already field reads, now strings; verify compiles.
- `ai_purchasing.go`: `"date": vp.PaymentDate`, `core["scheduled_date"] = *vp.ScheduledDate`, `"date": vc.CreditDate`. If `purchasingDate` / `purchasingDateLayout` then have no callers, delete them (and the `time` import if unused).
- `ai_inventory.go`: `"date": ct.TransferDate`. Keep `aiDateLayout` only if still referenced.
- Run `go build ./... && go vet ./...` to catch any remaining caller across the repo and fix it.

### Step 8 — dashboard recent entries (independent)
Files: `controllers/dashboard_accounting.go`, `controllers/dashboard_accounting_test.go`.
- `journalEntryOut.Date` → `string` (`json:"date"`), formatted in `mapRecentEntries` with a package-level const (e.g. `journalEntryDateLayout = "2006-01-02"`; reuse an existing controllers date-layout const if one exists).
- Replace the comment: the column is a DATE, so `Date` is a `yyyy-mm-dd` calendar date with no time of day.
- `TestMapRecentEntries`: assert `out[0].Date == "2026-09-04"`; fix the stale comment there too.
- `cashtransfer.RecentEntry` is unchanged.

### Step 9 — accountingperiod wire shape (independent)
Files: new `accountingperiod/json.go`, new `accountingperiod/json_test.go`. Do **not** change `types.go` field types or any logic.
- Const `dateLayout = "2006-01-02"`; helpers `formatDate(time.Time) string` and `formatDatePtr(*time.Time) *string` (nil → nil).
- Value-receiver `MarshalJSON` for `Period`, `Quarter`, `FiscalYear` (`start`, `end`), `Calendar` (`basePeriodStart`, `booksClosedThrough` as `*string` with `omitempty`) and `StatusChangeResult` (`booksClosedThrough` as `*string`, **no** omitempty — keep `null`), each via `type alias X; json.Marshal(struct{ alias; Start string \`json:"start"\` ... }{alias(x), ...})`.
- Single-line doc comment on every exported method.
- Table-driven tests (testify): each type's date keys are `yyyy-mm-dd`; nested `FiscalYear.Periods[0].start` and `.Quarters[0].start` are `yyyy-mm-dd`; `createdAt`/`closedAt`/`configuredAt` still RFC3339; nil `Calendar.BasePeriodStart` omitted; nil `StatusChangeResult.BooksClosedThrough` is `null`; all other existing keys still present.

### Step 10 — verify + review (sequential)
- `go build ./... && go vet ./... && go test ./...`.
- `go-verifier` on the diff; then `module-drift-checker` (payment, creditmemo, refund, vendorpayment, vendorcredit, cashtransfer — confirm the six are hand-ported identically) and `tenancy-security-reviewer` (controllers + store files touched) in one parallel batch.

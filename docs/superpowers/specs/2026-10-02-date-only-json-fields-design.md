# Date-only JSON response fields — design

**Date:** 2026-10-02
**Status:** proposed

## Problem

Several modules scan Postgres `DATE` columns into Go `time.Time` and marshal
them straight to JSON, so the wire value is a fake UTC-midnight timestamp
(`"2026-01-02T00:00:00Z"`) instead of the `"yyyy-mm-dd"` string that
`salesorder`, `invoice` and `inventory*` already return
(`salesorder/types.go`: `OrderDate string // "yyyy-mm-dd"`, scanned via
`to_char(col,'YYYY-MM-DD')`). Clients in negative-UTC-offset zones that parse
the timestamp naively render the previous day.

## Affected response fields

| Package | Field | Column | Internal logic use |
|---|---|---|---|
| `payment` | `Payment.PaymentDate` | `payment_date` | scan, cursor `sortValue`, AI core |
| `creditmemo` | `CreditMemo.CreditMemoDate` | `credit_memo_date` | scan, cursor, AI core |
| `refund` | `Refund.RefundDate` | `refund_date` | scan, cursor, AI core |
| `vendorpayment` | `VendorPayment.PaymentDate`, `.ScheduledDate` | `vendor_payment_date`, `vendor_payment_scheduled_date` | scan, cursor, PDF, AI core |
| `vendorcredit` | `VendorCredit.CreditDate` | `vendor_credit_date` | scan, cursor, PDF, AI core |
| `cashtransfer` | `CashTransfer.TransferDate` | `cash_transfer_date` | scan, cursor, AI core |
| `controllers` | `journalEntryOut.Date` (from `cashtransfer.RecentEntry.Date`) | `cash_transfer_date` | dashboard widget only |
| `accountingperiod` | `Calendar.BasePeriodStart/BooksClosedThrough`, `FiscalYear/Quarter/Period.Start/End`, `StatusChangeResult.BooksClosedThrough` | DATE columns | **heavy** — period ordering, lock rules, generation arithmetic, journal posting checks |

## Decisions

### D1 — Six document modules: domain field becomes `string`, scanned with `to_char` (salesorder pattern)

Their date fields are used only for scan → JSON, keyset cursor, PDF and AI core.
Changing the struct field to `string` (`*string` for nullable
`ScheduledDate`) and selecting `to_char(col,'YYYY-MM-DD')`
(`to_char(...)` without `COALESCE` for the nullable column so NULL → `nil`) is
exactly the clone-twin shape `salesorder`/`invoice` use, so the modules stay
consistent with their siblings.

- **Cursor:** `sortValue` now returns the `yyyy-mm-dd` string;
  `query.NextCursor` passes non-`time.Time` values through untouched and
  Postgres infers the `$n` param as `date` from the comparison — proven by
  `salesorder`'s `order_date`. Cursors minted before deploy carry an RFC3339
  string, which Postgres still casts to `date`, so in-flight pagination keeps
  working.
- **PDF:** `vendorpayment/printable.go` and `vendorcredit/printable.go` drop
  their `.Format("2006-01-02")` and use the string directly. `payment`,
  `refund`, `creditmemo`, `cashtransfer` have no `printable.go`.

### D2 — `accountingperiod`: keep `time.Time` internally, override the wire shape with `MarshalJSON`

`Start`/`End`/`BasePeriodStart` drive sorting, `Before`/`Equal` lock rules and
`AddDate` generation. Converting them to strings would rewrite that logic for a
pure presentation change. Instead add `accountingperiod/json.go` with
value-receiver `MarshalJSON` on `Calendar`, `FiscalYear`, `Quarter`, `Period`
and `StatusChangeResult`, using the alias-embedding pattern (outer shadow
fields with the same JSON name win over the embedded alias's fields). Nil
pointers keep their current semantics: `omitempty` on `Calendar` (omitted),
`null` on `StatusChangeResult.BooksClosedThrough`. `ConfiguredAt`, `ClosedAt`,
`CreatedAt`, `UpdatedAt` and `HistoryEntry.At` are real timestamps and are
**not** changed.

Out of scope: `controllers/accountingperiod_audit.go` writes `p.Start`/`p.End`
into audit-log maps (stored JSONB, not a response contract) — unchanged.

### D3 — Dashboard recent entries: format in the mapper

`cashtransfer.RecentEntry` is an internal, untagged struct; keep its
`Date time.Time` and have `controllers.mapRecentEntries` format with a
`"2006-01-02"` constant into `journalEntryOut.Date string`. Correct the comment
that wrongly calls it "a real timestamp" (the column is `DATE`; there is no
time-of-day to render "2h ago" from).

### D4 — Request/input types are unchanged

`payment`, `refund`, `vendorpayment`, `cashtransfer` inputs keep
`*time.Time` (the WebUI sends `toRFC3339OrUndefined(...)`); `creditmemo` and
`vendorcredit` inputs already take `yyyy-mm-dd` strings. No accept-both parsing
is added in this change — widening input parsing is a separate decision.

### D5 — AI / globalsearch `Core`

`globalsearch/ai_sales.go` currently puts raw `time.Time` into `core["date"]`
for payment/creditmemo/refund (RFC3339 in the rendered record) — it now gets
the string directly, which matches the shared `date` convention.
`ai_purchasing.go` drops `purchasingDate(...)` for the vendorpayment/
vendorcredit fields; the helper and `purchasingDateLayout` are deleted if no
caller remains. `ai_inventory.go` uses `ct.TransferDate` directly
(`aiDateLayout` stays if still used elsewhere). A blank/zero date previously
rendered `""` via `purchasingDate`; `to_char` on a NOT NULL column never yields
blank, and the nullable `scheduled_date` stays behind its existing `nil` check.

## Compatibility

The WebUI already accepts both shapes (`parseDateValue` treats exact UTC
midnight as date-only; `datePart()` / `fromRFC3339DateOnly()` slice to
`yyyy-mm-dd`), so the backend deploys independently. No schema change, no
route/RBAC change.

## Testing

- Update fixtures that set these fields to `time.Time`
  (`payment/types_test.go`, `cashtransfer/store_search_test.go`,
  `vendorpayment/printable_test.go`, `vendorcredit/printable_test.go`,
  `controllers/dashboard_accounting_test.go`).
- Add JSON-shape assertions that each affected field marshals as `"yyyy-mm-dd"`.
- Table-driven `accountingperiod/json_test.go` covering every overridden type,
  nil pointers (omitted vs `null`), nested `FiscalYear.Periods`/`Quarters`, and
  that non-date timestamps (`createdAt`, `closedAt`) are unchanged.

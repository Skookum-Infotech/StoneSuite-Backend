# Vendor transactions enhancements (3a–3e) — design

Builds on `vendortransactions/` (feed + summary, #427). Backend only; the Transactions-tab UI work in the ticket lives in `StoneSuite-WebUI` and is out of scope for this repo's PR (API shapes below are the contract).

## Facts found in the code
- **PO links.** `item_receipt.purchase_order_id`; `vendor_bill.vendor_bill_purchase_order_id` (single FK) plus `vendor_bill_conversion(vendor_bill_id, purchase_order_id)` for bills converted from a PO; payments reach a PO only via `vendor_payment_application(vendor_payment_id, vendor_bill_id)` → bill. No payment→PO column.
- **PO statuses:** `SENT → PART → RCVD → CLSD / CANC` (`purchaseorder/transitions.go`).
- **Scope plumbing** is `loadScope` / `granted` / `branchSQL` in `vendortransactions/`; the list, summary and every new endpoint must reuse it.
- **Today** = `controllers.tenantToday` (company_profile tz → `AUTOMATION_DEFAULT_TIMEZONE`), bound via `binder.today()`.
- **3e is already satisfied:** `globalsearch/providers_purchasing.go:20` registers `vendor` with `Domain:"purchases", Module:"vendor"` and `ai_purchasing.go:45` has `addAI("vendor", …)`. Work is verification tests only (see plan step 7).
- **No CSV code exists** outside vendored libs → stdlib `encoding/csv`. Rate limiter in `middleware/ratelimit.go` is per-tenant token bucket; export needs a per-user one.

## Decisions
1. **Aging "open" = the Ticket 1 definition** (`vendorBill().OpenWhere`: APPV/PART/ODUE and balance>0), not the ticket's looser "not void/paid/draft", because the acceptance criterion requires buckets to sum to the summary's open balance. PAPV bills are excluded in both.
2. **Aging buckets** by `due_date` vs tenant today: `current` (due ≥ today), `1-30`, `31-60`, `61+` days past due. Always returns all four buckets. Gated by `vendor_bill:read`; own-scope via the existing `granted.own`. Without bill read → 200 with `buckets: null` (consistent with summary's nil fields).
3. **Chain** `GET …/transactions/chain?po=<uuid>`: vendor via `authVendorByUUID`; PO must be live, belong to the vendor, and be readable under purchase_order scope, else 404. Downstream docs: receipts (by `purchase_order_id`), bills (FK ∪ conversion), payments (applications on those bills). Per type the caller gets `items` (only what they may read, own-scoped) and `hidden` (count of linked docs they may not see — never details). Derived `status`: `not_received` → `partially_received` (PO PART) → `received` (RCVD) → `billed` (≥1 visible non-void bill) → `paid` (all visible non-void bills fully paid). Billed/paid are computed from visible bills only; if bills are hidden the status stops at `received` and `billsHidden` is true, so hidden data never leaks through the status.
4. **Scheduled** (`vendor_payment` status `SCHD`): summary gains `scheduledOutflow` (SUM amount) and `scheduledCount`; new `GET …/transactions/scheduled?limit=` returns rows `{id, number, scheduledDate, amount}` ordered by date (default 5, max 100). Only `SCHD` rows, so cancelled/void are excluded by construction. Needs `vendor_payment:read`.
5. **Export** `GET …/transactions/export.csv` takes the list's query params (minus cursor/limit). It calls the same `parse` → `loadScope` → `buildPageSQL`/`fetchPage` path as `List` (shared `listAll` helper, limit `ExportMaxRows+1`); >10,000 rows → 413. Cells starting `= + - @` (also tab/CR) are prefixed with `'`. Audit row (actor, vendor uuid, filter set; no row data) via the existing audit helper; per-identity token-bucket limiter (429 + `Retry-After`). `Content-Disposition: attachment`.
6. **Security chain** on every route: tenantChain → `authVendorByUUID` (vendor:read + scope, 404 on denial) → per-type grants inside the package. Denials logged via `logSecurityEvent`.
7. **No schema change.**

## Intended behaviour recorded after review
- **Aging vs the ticket's wording.** The ticket says open = "balance > 0 and status not void, paid or draft". The implemented rule is the Ticket 1 summary's: status in APPV, PART, ODUE **and** balance_due > 0 (`vendorBill().OpenWhere`, `vendorbill.PayableStatuses`). The two differ only for **PAPV** (pending approval) bills, which the ticket wording would age but the summary does not count; they are not yet payable, so they are excluded from both. Aging reuses the summary's `is_open` predicate, which is what guarantees buckets sum to `openBalance`. A bill with no due date is treated as not past due (`current`). `ODUE`-status bills with a due date in the future still age by due date (the status flag does not move a bill between buckets), so a bill flagged overdue can sit in `current` until its due date passes.
- **Chain disclosure.** `hidden` is a count, never details. It is returned only when the caller has **no** read grant on the downstream module (the ticket's "hidden, not missing"). Own-scoped callers see only their own documents and `hidden` is 0, so they cannot learn how many records other owners linked. Derived status uses only what the caller can see (plus hidden receipts only when the caller has no receipt grant at all, which `hidden` already discloses).
- **Denials.** A missing read grant on a downstream type returns an empty/null section (filtering, not an error) and is logged as `permission_denied`; the chain's missing `purchase_order` grant logs `permission_denied` and still answers 404.

## Out of scope
Frontend work; payment→PO denormalisation; XLSX.

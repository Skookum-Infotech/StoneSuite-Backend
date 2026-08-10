# Vendor Bill Module — Backend Design Spec

**Date:** 2026-08-10
**Status:** Draft — awaiting approval before implementation.
**Branch:** `feat/vendor-bills`
**Scope:** New Vendor Bill module (header + line items + approval gate + bill-payment settlement ledger + optional Purchase Order lineage + attachments) for StoneSuite's Purchases/AP section. The accounts-payable mirror of `invoice/`, and the next link in the procurement chain: `requisition → purchase_order → item_receipt → vendor_bill`.

---

## 1. Overview & Goals

Add a Vendor Bill module — the document recording what a vendor has invoiced us, which an approver signs off and which is then settled by payments until its outstanding balance reaches zero. It follows the same v2 relational conventions as every sibling document module: hybrid PK (`SERIAL` + `UUID`), `employee`-based audit columns, paired soft delete, `record_version`, the RBAC/scope/IDOR chain, the `query/` filter engine, keyset pagination, and the AD-8 configuration-driven approval gate.

Structurally it is closest to `invoice/` (same status shape, same balance identity, same line shape), with `purchaseorder/`'s vendor-counterparty and approval conventions.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Vendor Bill record type | `lkp_record_type` row `VBIL` / `vendorbill` (**id 15**) — seeded, unused until now | `tenant/schema.sql:708` |
| Vendor Bill status lifecycle | `lkp_record_status` rows for `record_type=15`: `DRFT, PAPV ("Pending Approval"), APPV ("Approved"), PART ("Partially Paid"), PAID, ODUE ("Overdue"), VOID` — seeded, unused until now | `tenant/schema.sql:749` |
| RBAC resource + 5 actions | `authz.ResourceVendorBill` (create/read/update/delete/transition) — seeded, unused until now | `authz/catalog.go:50,309-313` |
| Generic-router mapping | `controllers/crm.go`'s `resourceForKey` already maps `"vendor_bill"` → `authz.ResourceVendorBill` | `controllers/crm.go:81` |
| Legacy v1 JSONB collision | A `workflows` row keyed `"vendor_bill"` already exists in the old JSONB engine (schema.sql:2008-2047, states `vb_draft…vb_void`). Same precedent as `vendor`/`sales_order`/`requisition`: **left untouched**; it doubles as this module's custom-field definition host. | `tenant/schema.sql:2008` |
| Vendor counterparty | `vendor` table + `vendors/` module; `vendorSnapshot` helper pattern | `purchaseorder/store.go:164` |
| PO lineage source | `purchase_order`, `purchase_order_item` | `tenant/schema.sql:4492` |
| SKU line source | `inventory_item` | `tenant/schema.sql` |
| Lookups | `lkp_unit`, `lkp_payment_terms`, `lkp_currency`, `lkp_tax_rate`, `lkp_payment_method` | `tenant/schema.sql` |
| Approval pattern (AD-8) | `purchase_order_approver`/`purchase_order_approval` + `purchaseorder/approval.go` | `purchaseorder/` |
| Balance / settlement math | `invoice/balance.go` — `PayableStatuses`, `Locked`, `LockForUpdate`, `DeriveStatus`, `RecomputeBalance`, documented global lock ordering | `invoice/balance.go` |
| Conversion-lineage pattern | destination-owns-the-write, idempotent replay; `purchaseorder.ConvertFromRequisition` | `purchaseorder/store_convert.go` |
| Line snapshot + tax resolution | `invoice/store_line_resolve.go` | `invoice/` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Record type / status resolution | `docflow.RecordTypeIDByCode`, `docflow.StatusIDByCode`, `docflow.InitialStatus` | `docflow/` |
| Audit log | `audit_logs` via `workflow.LogAuditFull` | `workflow/` |
| Row-level IDOR guard | `recordInScope(ctx, pool, scope, identityID, ownerUserID)` | `controllers/scope.go:29` |
| Attachments (storage, presign, caps, RBAC) | `workflow_record_attachments` (record-UUID keyed, FK deliberately dropped in migration 023) + `AttachmentOps` + the 5 generic routes | `workflow/attachments.go`, `controllers/attachments.go`, `main.go:474-478` |
| Auth skeleton (the correct one) | `controllers/purchaseorder.go` / `controllers/payment.go` — log `permission_denied`/`idor_denied`, 404-not-403 on scope denial | `controllers/` |

**Key finding that shaped this design:** exactly like Purchase Order and Requisition before it, Vendor Bill was already half-scaffolded — record type 15, its seven lifecycle statuses, the full RBAC catalog rows, the generic-router mapping, and a legacy JSONB workflow all exist and are unused. All are adopted **as-is**; this module adds **zero** new `lkp_record_type`/`lkp_record_status` seed rows and **zero** `authz/catalog.go` edits.

The seeded VBIL status set is deliberately invoice's set minus `SENT` — correct modelling, since a bill is received rather than sent. The design adopts that reading verbatim.

### What is genuinely missing (new tables — justified in §3)

- `vendor_bill`, `vendor_bill_item`, `vendor_bill_history`, `vendor_bill_approver`, `vendor_bill_approval`, `vendor_bill_payment`, `vendor_bill_conversion` — no existing table can represent a multi-line, approvable payable with a settlement ledger and PO lineage.
- No new lookup tables. No changes to any existing table.
- One additive branch in `workflow.ResolveRecordAccess` (AD-11) — not a schema change.

---

## 2. Architecture Decisions

**AD-1 — Relational, invoice/PO-shaped, not JSONB.** Same reasoning as every sibling financial document: stable line ids, FKs to `inventory_item`/`purchase_order_item`, an approval gate, and a settlement ledger — none of which the v1 JSONB engine offers. Custom fields still ride in `vendor_bill_custom_fields JSONB`, validated against the `"vendor_bill"` workflow key (the pre-existing legacy JSONB workflow doubles as the custom-field definition host; `workflow.GetWorkflowByKey` returning not-found is treated as "no custom fields configured", exactly as `purchaseorder.validateCustom` does).

**AD-2 — Vendor is fixed at creation.** `vendor_bill_vendor_id` is NOT NULL and never changes after create (mirrors PO's AD-2 and invoice's customer). The display name is snapshotted into `vendor_bill_vendor_name` at create time and never re-derived.

**AD-3 — Lines are SKU-or-free-text, with per-line discount and tax.** A line optionally carries `inventory_item_id` (nullable — bills routinely cover non-catalog costs such as freight, utilities or services); when absent, `description` is required. Money is the full `invoice_item` shape: `quantity`, `unit_price`, `discount_percent`, `tax_rate_id`/`tax_percent`, and stored `line_subtotal`/`line_discount`/`line_tax`/`line_total`. Catalog data is snapshotted at add/convert time and never re-read (the frozen-snapshot rule every sibling follows). A line may also carry a nullable `purchase_order_item_id` for traceability when it originated from a PO conversion.

**AD-4 — The Purchase Order link is optional and header-level.** `vendor_bill_purchase_order_id` is nullable (`ON DELETE SET NULL`). A bill may be raised standalone — the common case for non-inventory AP — or created from a PO via AD-8. There is **no `qty_billed` accounting and no over-billing guard**: full 3-way match (PO ↔ receipt ↔ bill) was explicitly considered and deferred, because it requires a non-additive change to the shipped `purchase_order_item` table and materially larger scope. The nullable link buys traceability now without that cost.

**AD-5 — Status machine (adopting the seeded VBIL codes verbatim; invoice's machine minus `SENT`):**

```
DRFT ──Submit──▶ PAPV ──Approve (AD-6 gate)──▶ APPV ──settlement──▶ PART ──settlement──▶ PAID
 │                │  │                          │  │                 │                    (terminal)
 │                │  └──Recall──▶ DRFT           │  └──▶ ODUE ◀───────┘
 │                │                              │        │
 └────────────────┴──────────────────────────────┴────────┴──▶ VOID (terminal)
```

Transition map:

| From | To |
|---|---|
| `DRFT` | `PAPV`, `VOID` |
| `PAPV` | `APPV`, `DRFT`, `VOID` |
| `APPV` | `PART`, `PAID`, `ODUE`, `VOID` |
| `PART` | `PAID`, `ODUE`, `VOID` |
| `ODUE` | `PART`, `PAID`, `VOID` |
| `PAID` | *(terminal)* |
| `VOID` | *(terminal)* |

`PART`/`PAID` are normally reached by **derivation** from the settlement ledger (AD-7), not by a user-directed transition; they remain in the map so an operator can correct state manually. `ODUE` is only ever set explicitly (by an operator or a future scheduled job) — it is a flag on an unpaid bill past its due date, not a settlement outcome. There is no `RJCT` status seeded for VBIL; rework is `PAPV → DRFT`, matching PO and Requisition.

**AD-6 — Approval is the configuration-driven gate, an exact structural copy of `purchase_order_approver`/`purchase_order_approval`.** Approvers are configured per `(record_type=VBIL, status=PAPV)`; zero configured approvers means the gate is open (`approval_status = 'none'`). Sign-off is idempotent per `(bill, status, approver)` and flips `vendor_bill_approval_status` to `'approved'` once the sign-off count reaches the configured approver count. `Approve` enforces `authz.ActionTransition`, matching the documented convention — `ActionApprove` is not granted to document modules, and `ResourceVendorBill`'s existing catalog rows already reflect this (5 rows, no `ActionApprove`). **No `authz/catalog.go` change is made**, deliberately.

**AD-7 — Settlement is a bill-owned payment ledger, not a separate AP payment module.** `vendor_bill_payment` is a direct child of `vendor_bill` (`amount > 0`, `payment_method_id` reusing `lkp_payment_method`, `reference_number`, `memo`, `paid_at`, paired soft delete). `POST /{uuid}/payment` records one; `DELETE /{uuid}/payments/{id}` soft-deletes it (the "unapply", matching `payment_application`'s convention). Both route through a single `RecomputeBalance`, which is the **sole writer** of the AP rollup:

```
balance_due = grand_total − amount_paid        (floored at zero)
amount_paid = SUM(live vendor_bill_payment.amount)
status      = DeriveStatus(current, amount_paid, grand_total)
```

Two copies of a financial invariant is how ledgers silently drift, so — exactly as `invoice/balance.go` documents — the identity lives in one place. Settlement is accepted only on `APPV`/`PART`/`ODUE` (`PayableStatuses`, the AP mirror of invoice's). **Overpayment is rejected, never silently clamped**, matching `payment.Apply`'s contract.

A future full `vendor_payment` module (its RBAC resource is already reserved) can adopt this ledger as its application table without a schema break — the same way `payment_application` became the source of truth for invoice AR.

**AD-8 — Conversion from a Purchase Order, destination-owns-the-write.** `vendorbill.ConvertFromPurchaseOrder` lives in the **`vendorbill`** package (raw SQL against `purchase_order`/`purchase_order_item`, no import of the `purchaseorder` Go package) — the convention `salesorder.ConvertFromQuote`, `invoice.ConvertFromSalesOrder` and `purchaseorder.ConvertFromRequisition` all follow. The HTTP route lives on the **source** controller (`POST /api/tenant/purchase-orders/{uuid}/convert-to-bill`, mirroring `controllers/requisition_convert.go`), checking `purchase_order:read` on the source (IDOR-guarded) and `vendor_bill:create` on the target inline. Every PO line is copied verbatim (never re-priced); header totals are recomputed from the copied lines. Only a received PO (`RCVD` or `CLSD`) may convert.

**Unlike Requisition→PO, a PO may be billed more than once.** Vendors routinely invoice a single order in installments, so `vendor_bill_conversion` is **UNIQUE on `vendor_bill_id` only, never on `purchase_order_id`** — each call creates a new bill, and each bill traces to exactly one conversion event. There is consequently no idempotent-replay short-circuit on this endpoint; that is a deliberate divergence from `ConvertFromRequisition`, forced by the business reality, and is called out here so it is not later mistaken for copy-paste drift.

**AD-9 — Money math mirrors `invoice/calc.go`.** Per line: `subtotal = qty × unitPrice`, `discount = subtotal × discountPercent/100`, `tax = (subtotal − discount) × taxPercent/100`, `total = subtotal − discount + tax`, each rounded to 2dp. Header sums the lines and applies `vendor_bill_adjustment`. A per-line `tax_rate_id`, when supplied, overrides the header's flat `vendor_bill_sales_tax_percent` for that line — identical to `invoice/store_line_resolve.go`.

**AD-10 — Numbering is `VBIL-000001`**, generated post-insert from the SERIAL id via `FormatNumber(int64)`, same convention as every sibling. This is *our* internal document number; the vendor's own invoice number is stored separately in `vendor_bill_vendor_invoice_number` (a free-text field, indexed for search — deliberately **not** globally unique, since two vendors may legitimately use the same invoice number).

**AD-11 — Attachments are wired now, via one additive branch.** The generic attachment stack (R2 storage, presign, per-file and per-record caps, RBAC + IDOR, audit) is already built and record-UUID keyed — `workflow_record_attachments` had its `workflow_records` FK deliberately dropped in migration 023 precisely so any table's records could use it. It resolves a record's type and owner through `workflow.ResolveRecordAccess`, which today knows four shapes (`workflow_records`, `customer`, `sales_order`, `cash_transfer`). This module adds a fifth branch for `vendor_bill`, mirroring the `cash_transfer` branch verbatim (owner resolved `employee → users.id`, no team column). Total cost ≈ 15 lines; the five existing routes then work unchanged for bill UUIDs, and `resourceForKey("vendor_bill")` already returns the right resource so the RBAC check is correct with no further edit.

This is a deliberate divergence from Requisition's AD-13, which deferred attachments. Attachments are an explicit requirement here, the mechanism already exists, and the increment is additive and precedented.

**AD-12 — Update is DRFT-only; Delete is soft, DRFT/VOID only.** Mirrors PO's AD-9/AD-10: once submitted for approval a bill is visible to an approver and keeps its trail. A bill with any live payment cannot be deleted regardless of status.

**AD-13 — No address block.** Unlike invoice (which snapshots bill-to/ship-to for print) and PO (which snapshots a ship-to for the vendor), a vendor bill is an inbound document that is never rendered and mailed to a counterparty. Adding address columns would be dead weight, so none are carried. The vendor's own address remains available on the `vendor` record.

**AD-14 — GL / journal posting is out of scope.** `journal_entry` and the accounting-period lock machinery exist, but no document module posts to them today, and posting was not part of this request. Deferred to a future cross-module accounting increment.

**AD-15 — Lock ordering.** `RecordPayment`/`RemovePayment` take `vendor_bill` last, after any `vendor_bill_payment` row lock — a fresh hierarchy (`vendor_bill_payment < vendor_bill`) that does not overlap the AR side's documented `credit_memo < payment < invoice`, so no cycle exists across the two and no cross-module deadlock is possible. Void cascades iterate ledger ids in ascending order, matching `invoice.LockForUpdateByID`'s rule.

---

## 3. Schema (7 new tables, all idempotent, appended at the end of `tenant/schema.sql`)

Appended as a `VENDOR BILL MODULE` section, following `/add-migration`'s rules — every statement idempotent and safe to re-run, since the whole file is applied in one `tx.Exec` on every boot and every provision.

| Table | Role |
|---|---|
| `vendor_bill` | Header — classification (`record_type`=VBIL, `vendor_bill_status`), `vendor_bill_approval_status` (`none`/`pending`/`approved` + CHECK), fixed vendor id + snapshot name, nullable `vendor_bill_purchase_order_id`, `vendor_bill_vendor_invoice_number`, `vendor_bill_reference_number`, `vendor_bill_date`, `vendor_bill_due_date`, payment terms / currency / exchange rate, owner (IDOR scope column), memo/notes/internal notes, money summary (`subtotal`, `discount_total`, `tax_total`, `adjustment`, `grand_total`), AP rollup (`amount_paid`, `balance_due`), `vendor_bill_custom_fields JSONB NOT NULL DEFAULT '{}'`, standard audit + paired soft delete + `record_version` |
| `vendor_bill_item` | Lines — `line_number`, nullable `inventory_item_id`, nullable `purchase_order_item_id` (`ON DELETE SET NULL`), snapshot `sku`/`item_name`/`description`/`unit_id`/`unit_code`, `quantity`, `unit_price`, `discount_percent`, `tax_rate_id`, `tax_percent`, stored line money, soft delete |
| `vendor_bill_history` | Status/action trail — mirrors `invoice_history`/`purchase_order_history` (`from_status_id`, `to_status_id`, `action`, `actor_employee_id`, `snapshot`, `at`) |
| `vendor_bill_approver` | AD-6 config — exact structural copy of `purchase_order_approver`, UNIQUE `(record_type_id, record_status_id, approver_employee_id)` |
| `vendor_bill_approval` | AD-6 sign-offs — exact structural copy of `purchase_order_approval`, UNIQUE `(vendor_bill_id, record_status_id, approver_employee_id)` |
| `vendor_bill_payment` | AD-7 settlement ledger — `amount` (CHECK > 0), `payment_method_id` → `lkp_payment_method`, `reference_number`, `memo`, `paid_at`, paired soft delete |
| `vendor_bill_conversion` | AD-8 lineage — `purchase_order_id`, `vendor_bill_id` **UNIQUE**, `converted_at`/`converted_by`, `{poItemUuid: billItemUuid}` snapshot JSONB. Deliberately *not* unique on `purchase_order_id` |

**Constraints:** `uq_vendor_bill_uuid`, `uq_vendor_bill_number`, `chk_vbil_approval_status`, `chk_vbil_tax_percent` (0–100), `chk_vbil_totals_nonneg`, `chk_vbil_paid_nonneg`, `chk_vbil_soft_delete` (paired), plus the line-level `chk_vbi_*` set mirroring `chk_ii_*`. All new CHECKs live inside their `CREATE TABLE` bodies, so the "`ADD CONSTRAINT` is not idempotent" trap does not apply.

**Indexes** (all partial on live rows, mirroring `purchase_order`'s set): vendor, status, PO link, bill date, due date, owner; keyset pairs `(created_at, id)`, `(updated_at, id)`, `(due_date, id)`, `(grand_total, id)`, `(balance_due, id)`; GIN on `vendor_bill_custom_fields`; `uq_vbi_line_active` unique partial on `(vendor_bill_id, line_number) WHERE item_deleted_at IS NULL`; approver lookup on `(record_type_id, record_status_id) WHERE is_active`; ledger index on `vendor_bill_id`.

**Zero** new `lkp_record_type` / `lkp_record_status` seed rows. **Zero** changes to any existing table.

---

## 4. Package Layout (`vendorbill/`)

Mirrors `invoice/`'s anatomy, split by verb from the start (the repo cap is 300 lines/file; `salesorder`'s 42 KB monolith is the counter-example, not the model).

| File | Role | Pure? |
|---|---|---|
| `types.go` | `CreateVendorBillInput`, `UpdateVendorBillInput`, shared `vendorBillFields` embed, `LineInput`, `Line`, `VendorRef`, `PurchaseOrderRef`, `BillPayment`, `VendorBill`, `Page` | ✅ |
| `calc.go` | `ComputeLine`, `ComputeHeader`, `round2` (AD-9) | ✅ |
| `numbering.go` | `numberPrefix = "VBIL"`, `FormatNumber` (AD-10) | ✅ |
| `transitions.go` | `allowedTransitions`, `CanTransition`, `ValidateTransition`, `ErrInvalidTransition` (AD-5) | ✅ |
| `resolver.go` | filter / sort / search whitelists for `query/`, `cf:` escape hatch | ✅ |
| `balance.go` | `PayableStatuses`, `Locked`, `BalanceDue`, `LockForUpdate`, `LockForUpdateByID`, `DeriveStatus`, `RecomputeBalance` (AD-7, AD-15) | mixed |
| `approval.go` | `activeApproverCount`, `signOffCount`, `isConfiguredApprover`, `Approve`, `ErrNotApprover`, `ErrApprovalRequired`, `ErrApprovalNotRequired` (AD-6) | mixed |
| `store.go` | shared helpers + `Get`: `ClientError`, `buildInsert`, `buildUpdateSet`, `vendorSnapshot`, `scanBill`, `loadLines`, `ErrNotFound` | ❌ |
| `store_create.go` | `Create` | ❌ |
| `store_update.go` | `Update` (DRFT-only; **`custom_fields` nil-guard**) | ❌ |
| `store_search.go` | `Search` (scope ANDed, keyset) | ❌ |
| `store_transition.go` | `Transition` | ❌ |
| `store_delete.go` | `SoftDelete` (DRFT/VOID only, blocked by live payments) | ❌ |
| `store_line_resolve.go` | line snapshot + tax resolution (mirrors `invoice/store_line_resolve.go`) | ❌ |
| `store_payment.go` | `RecordPayment`, `RemovePayment`, `ListPayments` (AD-7) | ❌ |
| `store_convert.go` | `ConvertFromPurchaseOrder` (AD-8) | ❌ |

Tests: `calc_test.go`, `numbering_test.go`, `transitions_test.go`, `resolver_test.go`, `balance_test.go` (table-driven, stdlib `testing` — the repo convention for pure functions), plus `store_test.go` with `//go:build dbtest` on line 1 **and** the `TEST_DATABASE_URL` skip guard.

## 5. Controllers

`controllers/vendorbill.go` (auth skeleton copied from `controllers/purchaseorder.go` — the corrected one: logs `permission_denied`/`idor_denied`, 404-not-403 on scope denial), `controllers/vendorbill_audit.go`, `controllers/vendorbill_payments.go`, and `controllers/purchaseorder_convert.go` (AD-8, on the source controller).

```
GET    /api/tenant/vendor-bills                              vendor_bill:read        list (cursor-paginated)
POST   /api/tenant/vendor-bills/search                       vendor_bill:read        filter + sort + search + pagination
POST   /api/tenant/vendor-bills                              vendor_bill:create      create (+ lines)
GET    /api/tenant/vendor-bills/{uuid}                       vendor_bill:read        get (+ lines, + payments)
PATCH  /api/tenant/vendor-bills/{uuid}                       vendor_bill:update      update (DRFT only — AD-12)
DELETE /api/tenant/vendor-bills/{uuid}                       vendor_bill:delete      soft delete (DRFT/VOID only — AD-12)
POST   /api/tenant/vendor-bills/{uuid}/transition            vendor_bill:transition  body {"toStatusCode":"PAPV"}
POST   /api/tenant/vendor-bills/{uuid}/approve               vendor_bill:transition  AD-6 sign-off
POST   /api/tenant/vendor-bills/{uuid}/payment               vendor_bill:update      AD-7 record settlement
GET    /api/tenant/vendor-bills/{uuid}/payments              vendor_bill:read        AD-7 AP reconciliation view
DELETE /api/tenant/vendor-bills/{uuid}/payments/{paymentId}  vendor_bill:update      AD-7 unapply
GET    /api/tenant/vendor-bills/{uuid}/audit                 vendor_bill:read        unified audit trail
POST   /api/tenant/purchase-orders/{uuid}/convert-to-bill    purchase_order:read + vendor_bill:create   AD-8
```

`POST /{uuid}/payment` body: `{"amount": 500.00, "methodId": 3, "referenceNumber": "CHK-1042", "memo": "", "paidAt": "2026-08-10"}`.

Attachments need **no new routes** — the existing generic five (`main.go:474-478`) start serving bill UUIDs once AD-11's branch lands.

**Error mapping** (`vendorBillFail`): `ErrNotFound` → 404; `ErrInvalidTransition`/`ErrApprovalRequired`/`ErrApprovalNotRequired` → 409; `ErrNotApprover` → 403; `ClientError` → 400; `*query.InvalidFilterError` → **400, never 500**; default → 500.

**Filterable fields:** `id`, `document_number`, `record_number`, `vendor_id`, `purchase_order_id`, `status`, `owner_id`, `vendor_invoice_number`, `bill_date`, `due_date`, `currency_id`, `payment_terms_id`, `grand_total`, `amount_paid`, `balance_due`, `created_at`, `updated_at`, `created_by`, `updated_by`, plus `cf:<key>`.
**Sortable** (stable non-null only — nullable columns break keyset comparison): `document_number`, `record_number`, `bill_date`, `grand_total`, `balance_due`, `status`, `vendor_id`. `due_date` stays filterable but **not** sortable, matching invoice's documented reasoning.
**Search predicate:** bill number, vendor invoice number, memo, notes, vendor snapshot name, line sku/item name, and the vendor's legal/given/family name.

## 6. RBAC / Generic Router

**No changes needed.** `authz.ResourceVendorBill` already carries its 5 catalog rows at `authz/catalog.go:309-313`, and `controllers/crm.go`'s `resourceForKey` already maps `"vendor_bill"`. Per AD-6, `ActionApprove` is deliberately **not** added.

## 7. Shared-File Edits (3)

| File | Edit |
|---|---|
| `database/migrations/tenant/schema.sql` | Append the `VENDOR BILL MODULE` section: 7 tables + ~14 indexes. Zero seed stanzas. |
| `main.go` | One `controllers.NewVendorBillOps()` + 13 `mux.Handle` lines, every one wrapped in `tenantChain`. |
| `workflow/attachments.go` | AD-11: one `vendor_bill` branch in `ResolveRecordAccess`, mirroring the `cash_transfer` branch. |

`authz/catalog.go` needs **no** edit (§6).

---

## 8. Implementation Plan

1. **Schema** — append the `VENDOR BILL MODULE` section; prove idempotency by applying twice against a fresh pgvector database.
2. **Pure logic + tests first** — `types.go`, `calc.go`, `numbering.go`, `transitions.go`, `resolver.go` with table-driven tests. No database needed; cheapest place to be correct.
3. **Store core** — `store.go` + `Get`, then `store_create.go`, `store_update.go`, `store_search.go`, `store_transition.go`, `store_delete.go`, `store_line_resolve.go`.
4. **Balance + settlement** — `balance.go`, then `store_payment.go` (AD-7, AD-15).
5. **Approval** — `approval.go` (AD-6).
6. **Conversion** — `store_convert.go` + `controllers/purchaseorder_convert.go` (AD-8).
7. **Controllers + routes** — the three `vendorbill*` controller files, then `main.go` wiring.
8. **Attachments** — the `ResolveRecordAccess` branch (AD-11).
9. **DB-backed tests** — `store_test.go` under the `dbtest` tag.
10. **Verify** — `go build ./... && go vet ./... && go test ./...`; then apply the schema twice against a fresh pgvector DB and run `go test -tags dbtest ./vendorbill/...`.
11. **Review** — `module-drift-checker`, then `tenancy-security-reviewer`, then `migration-auditor` on the diff.

## 9. Security Invariants (non-negotiable)

Enforced throughout, per CLAUDE.md:

- Every handler goes through `authVendorBill(...)`; every single-record handler through `authVendorBillByUUID(...)`. No exceptions.
- Scope denial returns **404, not 403**, so ids cannot be enumerated.
- `permission_denied` and `idor_denied` are logged via `logSecurityEvent`. Never log raw tokens or secrets.
- Filter ⨯ scope is **ANDed, never OR** — a user filter can only narrow the caller's permitted set.
- Field keys are a whitelist via `query.FieldResolver`; an unresolved key is 400, never raw SQL. All values parameterized (`$n`).
- Pagination is keyset (opaque base64 cursor), capped at `MaxLimit`.
- Tenant isolation is by construction (database-per-tenant); every route sits behind `tenantChain`, so `TenantResolver` is never bypassed.
- `custom_fields` is nil-guarded before update — the column is `NOT NULL DEFAULT '{}'`, and a nil map encodes as SQL NULL, 500-ing every PATCH that omits it.
- Every mutation runs in a transaction; `RecomputeBalance` is the sole writer of the AP rollup.

## 10. Deliberate Divergences from Sibling Modules

Recorded here so a future drift check does not misread them as copy-paste errors:

| Divergence | Reason |
|---|---|
| No `SENT` status | A bill is received, not sent. The seeded VBIL status set already reflects this. |
| No address block (AD-13) | An inbound document is never rendered and mailed to a counterparty. |
| `vendor_bill_conversion` not unique on `purchase_order_id` (AD-8) | A PO is legitimately billed in installments — unlike Requisition→PO, which is one-shot. |
| No idempotent-replay short-circuit on convert (AD-8) | Follows directly from the above. |
| Attachments wired (AD-11) vs Requisition's AD-13 deferral | Explicitly requested; mechanism already exists; increment is additive and precedented. |
| Settlement ledger owned by the bill (AD-7) rather than a separate applications module | No AP payment module exists; a bill-owned ledger makes the requested payment status and outstanding balance reachable today, and upgrades cleanly later. |

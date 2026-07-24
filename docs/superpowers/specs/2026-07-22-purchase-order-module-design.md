# Purchase Order Module — Backend Design Spec

**Date:** 2026-07-22
**Status:** Draft — approved direction from planning session (relational store, line items with SKU + free-form, workflow-configured approvals), proceeding to implementation.
**Scope:** New Purchase Order module (header + line items + receiving progress + status workflow + approval gate) for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend. First module of the Purchases section beyond the existing `vendors/` directory module. Item Receipt, Vendor Bill, and the rest of the Purchases chain are **out of scope** here, but this design deliberately leaves the hooks they need (stable line-item ids, `qty_received` columns, receipt-driven status rollup helper).

---

## 1. Overview & Goals

Add a production-grade **Purchase Order** module — a multi-line, approvable document sent to a single vendor, tracking ordered vs. received quantities — as a sibling of `estimate`/`quote`/`salesorder`/`invoice`, following the same v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), employee-based audit columns, paired soft delete, `record_version`, RBAC/scope/IDOR chain, the `query/` filter engine, keyset pagination, and the AD-8 configuration-driven approval gate.

Today the Purchases section has only the `vendors/` directory. Every downstream purchasing document (Item Receipt → Vendor Bill → Vendor Payment/Credit) hangs off a PO, so PO is the correct first document module.

**Non-negotiable constraints (from CLAUDE.md, identical to the sales document modules):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK, `employee(employee_id)`-based audit columns, paired soft delete, `record_version`.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route; `permission_denied` + `idor_denied` security logging (payment.go skeleton); scope denial returns **404**, never 403.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).
- Custom fields ride in a `JSONB` column validated against the `purchase_order` workflow's `workflow_field_definitions` (≤15), exactly as `invoice.validateCustom` does.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| PO record type | `lkp_record_type` row `PORD` / `purchaseorder` (**id 13**) — seeded, unused until now | `tenant/schema.sql:706` |
| PO status lifecycle | `lkp_record_status` rows for `record_type=13`: `DRFT, PAPV, APPV, SENT ("Sent to Vendor"), PART ("Partially Received"), RCVD ("Received"), CLSD ("Closed"), CANC` — seeded, unused until now | `tenant/schema.sql:748-750` |
| RBAC resource + 5 actions | `authz.ResourcePurchaseOrder` (create/read/update/delete/transition) | `authz/catalog.go:48,195-199` |
| Vendor (the counterparty) | `vendor` table + `vendors/` module | `tenant/schema.sql:3425`, `vendors/` |
| SKU line source | `inventory_item` (sku, name, description, unit, price, tax) | `tenant/schema.sql:2376` |
| Lookups | `lkp_unit`, `lkp_currency`, `lkp_tax_rate`, `lkp_payment_terms`, `lkp_country`, `lkp_state` | `tenant/schema.sql` |
| Custom-field definitions + validation | `purchase_order` JSONB workflow (seeded, schema.sql:1932) + `workflow.ValidateCustomFieldsPartial` | `workflow/validate.go` |
| Approval pattern (AD-8) | `estimate_approver`/`estimate_approval` + `estimate/approval.go` | `estimate/` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Money calc pattern | `estimate/calc.go` (`ComputeLine`/`ComputeHeader`, `round2`) | `estimate/` |
| Audit log | `audit_logs` via `workflow.LogAuditFull` | `tenant/schema.sql:1281` |
| Row-level IDOR guard | `recordInScope(ctx, pool, scope, identityID, ownerUserID)` | `controllers/scope.go:29` |
| Auth skeleton (the correct one) | `controllers/payment.go` — the only controller that logs `permission_denied` | `controllers/payment.go` |
| Line snapshot + resolve pattern | `invoice/store_line_resolve.go` (`resolveInventoryItem`) | `invoice/` |

> **Key finding that shaped this design:** like Refund before it, PO was already half-scaffolded — record type 13, all eight lifecycle statuses, and the full RBAC catalog rows exist and are unused. The `lkp_record_status` seed keys statuses to record types by hardcoded integer relying on `SERIAL` order, so it is adopted **as-is** (notably: there is no `RJCT` status for PORD — rework is modeled as `PAPV → DRFT`, matching how a recalled document re-enters draft).

### What is genuinely missing (new tables — justified in §3)

- `purchase_order`, `purchase_order_item`, `purchase_order_history`, `purchase_order_approver`, `purchase_order_approval` — no existing table can represent a multi-line, approvable, receivable purchasing document addressed to a vendor.
- No new lookup tables. No changes to any existing table.

---

## 2. Architecture Decisions

**AD-1 — Relational, estimate/quote-shaped (money + lines + approval), not JSONB.** Line items must FK to `inventory_item` for SKU lines, carry stable ids for future Item Receipts to reference (`purchase_order_item_id`), and accumulate `qty_received` — none of which the JSONB engine can do. Custom fields still ride along in `purchase_order_custom_fields JSONB` validated against the seeded `purchase_order` workflow's field definitions (≤15), so tenants keep dynamic fields on top of the relational core.

**AD-2 — Single vendor per PO, snapshot-copied.** `purchase_order_vendor_id` FKs `vendor(vendor_id)`; display name is snapshotted into `purchase_order_vendor_name` at create/update time (same never-re-derive rule as invoice's billing snapshot — a later vendor rename must not rewrite issued POs).

**AD-3 — Lines are SKU or free-form, snapshot-priced.** A line optionally carries `inventory_item_id`. When present, sku/name/description/unit/price default from the catalog item (caller can override price); when absent, the line is free-form (description required). Either way the line stores its own snapshot — catalog edits never rewrite existing POs. Mirrors `invoice_item` resolution exactly.

**AD-4 — Receiving progress lives on the line now; receiving *acts* later.** Each line carries `po_item_qty_received DECIMAL NOT NULL DEFAULT 0` and the header derives `SENT → PART → RCVD` from `qty_received` vs `qty_ordered` totals. In this module the receiving quantities are only ever written by the future Item Receipt module (there is **no** public "receive" endpoint here); the `PART`/`RCVD` statuses are reachable manually via transition for tenants operating without receipts, and automatically later. A `RollupReceiptStatus` helper is built and unit-tested now so Item Receipt can drive it.

**AD-5 — Status machine (adopting the seeded codes verbatim):**

```
DRFT ──Submit──▶ PAPV ──Approve (AD-8 gate)──▶ APPV ──Send──▶ SENT ──▶ PART ──▶ RCVD ──▶ CLSD
 │  ▲              │                             │              │        │
 │  └──Recall──────┘ (PAPV→DRFT, rework)         └─Revise→DRFT  │        └────Short-close──▶ CLSD
 └────────────▶ CANC   (also PAPV→CANC, APPV→CANC, SENT→CANC)   └────Short-close──▶ CLSD
```

- `CLSD` and `CANC` are terminal.
- Short-close (`SENT/PART → CLSD`) is the industry-standard "close with unreceived quantity".
- `CANC` is only reachable before any receiving (`DRFT/PAPV/APPV/SENT`); once goods have arrived (`PART`), the PO can only be short-closed, never cancelled.

**AD-6 — Approval is the AD-8 configuration-driven gate, exact structural copy of `estimate_approver`/`estimate_approval`.** Approvers are configured per (record_type=PORD, status=PAPV); zero configured approvers means the gate is open (`approval_status='none'`, `PAPV → APPV` is a plain transition). The `Approve` endpoint enforces `authz.ActionTransition` per existing convention (see module-anatomy note — `ActionApprove` is not granted to document modules).

**AD-7 — Money math is estimate's `calc.go` verbatim:** per line `qty × unitPrice − discount% + tax%` rounded to 2dp; header totals subtotal/discount/tax + shipping charge + adjustment = grand total. No new invariants.

**AD-8 — Numbering is `PORD-000001`,** generated post-insert from the SERIAL id like every sibling (`FormatNumber`), with the tenant-facing prefix overridable later through the existing `workflow_numbering_configs` mechanism if the tenant enables it.

**AD-9 — Delete is soft, and guarded.** Only `DRFT` and `CANC` POs may be deleted (a document the vendor may have received must keep its trail). Mirrors invoice's delete guard.

**AD-10 — Update is DRFT-only.** The whole document (header + lines) is editable only in `DRFT`; once submitted/approved/sent it is in (or on its way to) the vendor's hands. Rework flows through the recall transitions (`PAPV → DRFT`, `APPV → DRFT`), edit, then resubmit. This is deliberately stricter than estimate (which allows edits in any non-terminal status) — a PO is an outward commitment to a counterparty.

---

## 3. Schema (5 new tables, all idempotent, appended at the end of tenant/schema.sql)

### 3.1 `purchase_order` (header)

Standard v2 header per the wiring template: `purchase_order_id SERIAL PK`, `purchase_order_uuid UUID UNIQUE`, `purchase_order_number VARCHAR(20) UNIQUE` (nullable pre-numbering), `record_type → lkp_record_type` (=PORD), `purchase_order_status → lkp_record_status`, `purchase_order_approval_status ∈ none|pending|approved`.

Domain columns:
- `purchase_order_vendor_id → vendor(vendor_id)` + `purchase_order_vendor_name` snapshot (AD-2)
- `purchase_order_owner_id → employee` (IDOR scope owner)
- `purchase_order_date DATE NOT NULL DEFAULT CURRENT_DATE`, `purchase_order_expected_date DATE NULL`
- `purchase_order_reference_number VARCHAR(50)` (vendor's quote/ref)
- `purchase_order_payment_terms → lkp_payment_terms NULL`, `purchase_order_currency → lkp_currency NULL`, `purchase_order_exchange_rate DECIMAL(15,6) DEFAULT 1`
- Ship-to snapshot block (deliver-to: name/attention/line1/line2/suite/city/state FK/zip/country FK/phone/email) — the buyer's receiving address, single block (POs have no billing/shipping pair; the bill-to is the tenant itself)
- `purchase_order_memo/_notes/_internal_notes/_terms_conditions TEXT DEFAULT ''`
- Totals: `_subtotal/_discount_total/_tax_total/_shipping_charge/_adjustment/_grand_total DECIMAL(15,2) DEFAULT 0`
- `purchase_order_custom_fields JSONB NOT NULL DEFAULT '{}'`
- Standard audit tail + `chk` soft-delete pair + `chk_purchase_order_approval_status`.

### 3.2 `purchase_order_item` (lines) — the receiving hook

`purchase_order_item_id SERIAL PK`, `purchase_order_item_uuid UUID UNIQUE`, `purchase_order_id → purchase_order ON DELETE CASCADE`, `po_item_line_number INT`,
`inventory_item_id → inventory_item NULL` (SKU line) — plus snapshot columns `po_item_sku/_name/_description`, `po_item_unit_id → lkp_unit NULL`,
`po_item_quantity DECIMAL(15,4) > 0`, `po_item_qty_received DECIMAL(15,4) NOT NULL DEFAULT 0` (AD-4, `0 ≤ received ≤ quantity` CHECK),
`po_item_unit_price DECIMAL(15,4) ≥ 0`, `po_item_discount_percent`, `po_item_tax_rate_id → lkp_tax_rate NULL`, `po_item_tax_percent`,
stored money `po_item_subtotal/_discount/_tax/_total DECIMAL(15,2)`.

### 3.3 `purchase_order_history` — status trail, mirrors `estimate_history` (from/to status, changed_by employee, note, timestamp).

### 3.4 / 3.5 `purchase_order_approver` / `purchase_order_approval` — exact structural copies of `estimate_approver`/`estimate_approval` keyed (record_type_id=PORD, record_status_id, approver_employee_id) with the same uniques and partial index.

Indexes: partial live-row indexes on vendor/status/owner, GIN on custom fields, keyset pairs on `(created_at, purchase_order_id)`, `(updated_at, purchase_order_id)`; item index on `(purchase_order_id)`; history index on `(purchase_order_id)`.

Seeds: **none needed** — record type, statuses, and the `purchase_order` workflow (custom-field host) are already seeded. This module adds zero seed stanzas, so the hardcoded-integer status trap is not in play.

---

## 4. API surface (all `tenantChain`-wrapped, payment.go auth skeleton)

| Route | Handler | RBAC |
|---|---|---|
| `GET    /api/tenant/purchase-orders` | List | `purchase_order:read` + scope |
| `POST   /api/tenant/purchase-orders/search` | Search (query/ engine) | `purchase_order:read` + scope |
| `POST   /api/tenant/purchase-orders` | Create | `purchase_order:create` |
| `GET    /api/tenant/purchase-orders/{uuid}` | Get | `purchase_order:read` + IDOR |
| `PATCH  /api/tenant/purchase-orders/{uuid}` | Update | `purchase_order:update` + IDOR |
| `DELETE /api/tenant/purchase-orders/{uuid}` | Delete (soft, AD-9) | `purchase_order:delete` + IDOR |
| `POST   /api/tenant/purchase-orders/{uuid}/transition` | Transition | `purchase_order:transition` + IDOR |
| `POST   /api/tenant/purchase-orders/{uuid}/approve` | Approve (AD-6) | `purchase_order:transition` + IDOR |
| `GET    /api/tenant/purchase-orders/{uuid}/audit` | Audit trail | `purchase_order:read` + IDOR |

`resourceForKey` in `controllers/crm.go` is **not** touched — the generic JSONB router does not serve purchase orders (the seeded `purchase_order` workflow exists solely as the custom-field definition host, same relationship invoice has with its workflow).

Filter/sort whitelist (`resolver.go`): `status`, `vendorId`, `vendorName`, `poNumber`, `referenceNumber`, `orderDate`, `expectedDate`, `grandTotal`, `createdAt`, `updatedAt`, `ownerId` + `custom.<key>` escape hatch. Sortable: `created_at`, `updated_at`, `record_number` (repo rule).

## 5. Package layout (`purchaseorder/`, every file ≤300 lines)

`types.go`, `calc.go`, `numbering.go`, `transitions.go`, `resolver.go`, `approval.go`, `store.go` (helpers + Get), `store_create.go`, `store_update.go`, `store_search.go`, `store_transition.go`, `receipt_rollup.go` (AD-4 helper) + table-driven stdlib tests for every pure file + `store_test.go` (`//go:build dbtest`).

Controllers: `controllers/purchaseorder.go` + `controllers/purchaseorder_audit.go`.

## 6. Phases

1. Schema tables (§3) → CI schema-apply green.
2. Pure logic + tests (calc, numbering, transitions, resolver, receipt rollup).
3. Types + store + approval.
4. Controllers + routes.
5. dbtests + full verification (build/vet/test + fresh pgvector DB double-apply + dbtest).
6. Review agents: module-drift-checker → tenancy-security-reviewer → migration-auditor.

**Deferred (future modules, hooks left ready):** Item Receipt (writes `po_item_qty_received`, drives PART/RCVD via `RollupReceiptStatus`), Vendor Bill 3-way match (needs `po_item` stable ids — present), PO→Receipt conversion endpoint, PDF/email send.

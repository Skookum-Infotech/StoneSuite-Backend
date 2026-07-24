# Item Receipt Module — Backend Design Spec

**Date:** 2026-07-23
**Status:** Implemented on `friday-work-planner`.
**Scope:** New Item Receipt module (header + lines + posting + reversal + generic inventory ledger) for the StoneSuite multi-tenant, database-per-tenant backend. Second document module of the Purchases section, after Purchase Order. Vendor Bill, Vendor Payment and serialized slab intake are **out of scope**, with hooks left for them.

---

## 1. Overview & Goals

Add the **Item Receipt** — the document recording goods physically arriving against a finalized purchase order. It is the only writer of `purchase_order_item.qty_received`, the trigger for the purchase order's `SENT → PART → RCVD` rollup, and the first writer of a new generic inventory ledger.

**Key finding that shaped this design:** like Purchase Order and Refund before it, Item Receipt was already half-scaffolded, and PR #76 went further — it left working code for this module to call.

| Pre-existing asset | Location | State before this work |
|---|---|---|
| `purchase_order_item.qty_received` | `tenant/schema.sql` (PO block) | Column existed, commented *"written by Item Receipt postings"*, **zero writers** |
| `purchaseorder.RollupReceiptStatus` | `purchaseorder/receipt_rollup.go` | Pure + unit-tested, **zero callers** |
| PO transitions `SENT→PART`, `SENT→RCVD`, `PART→RCVD` | `purchaseorder/transitions.go` | Allowed, only manually reachable |
| `lkp_record_type` `IRCT` (id 14) | `tenant/schema.sql:707` | Seeded, unused |
| `lkp_record_status` type 14: `PEND`/`PART`/`RCVD`/`VOID` | `tenant/schema.sql:~756` | Seeded, unused |
| `authz.ResourceItemReceipt` + 5 actions | `authz/catalog.go` | Seeded, unused |
| `item_receipt` workflow (custom-field host) | `tenant/schema.sql:1973` | Seeded, unused |

**Non-negotiable constraints (CLAUDE.md):** database-per-tenant, no `tenant_id`; v2 relational conventions; idempotent append-only migrations; the mandatory `/api/tenant/` security chain with `permission_denied` + `idor_denied` logging and **404 (not 403)** on scope denial; all list/search through `query/`; custom fields validated against `workflow_field_definitions` (≤15); files ≤300 lines.

---

## 2. Architecture Decisions

**AD-1 — Adopt the four seeded IRCT statuses verbatim.** `PEND` (created, not posted — the editable state) → `PART`/`RCVD` (posted) → `VOID` (reversed, terminal). No new `lkp_record_status` seed rows, so the hardcoded-`record_type`-integer trap in that seed stays out of play. This is the same call the PO module made, and it means the module adds **zero seed stanzas**. A receipt line always FKs a `purchase_order_item` — there is no ad-hoc receiving.

**AD-2 — Vendor is inherited, never supplied.** The receipt takes its vendor id and name snapshot from the purchase order, so a receipt can never name a different counterparty than the order it settles. Only POs in `SENT` or `PART` may be received against.

**AD-3 — Over-delivery is allowed to a tolerance, with an override beyond it.** PR #76 shipped `chk_poi_qty_received CHECK (qty_received <= quantity)`, which makes an over-delivery literally unrecordable. Receiving 102 of 100 ordered is a real warehouse event, so that ceiling was **relaxed to non-negativity** and the rule moved into Go: `itemreceipt.WithinTolerance` accepts up to `OverReceiptTolerancePercent` (5%) silently; beyond it, the caller needs the `item_receipt:approve` grant *and* must record a reason. The check is **cumulative** across prior receipts — three 40-unit arrivals against a 100-unit order trip the gate on the third, though no single one exceeds the order.

**AD-4 — Warehouse is header-level, required, defaulted.** The purchase order carries no warehouse, so the receipt supplies it: `item_receipt.warehouse_id NOT NULL REFERENCES lkp_warehouse`, defaulted from `lkp_warehouse.warehouse_is_default`. One delivery, one dock, one receipt; a split shipment is two receipts.

**AD-5 — Posted receipts are immutable; correction is void-and-reissue.** `PATCH` and `DELETE` work only in `PEND` (plus `DELETE` in `VOID`). Once quantities have moved through the ledger, editing in place would leave the ledger describing a document that no longer exists. Voiding reverses `qty_received`, writes compensating ledger rows, and re-runs the rollup.

**AD-6 — Rejected goods are recorded but neither stock nor settle.** `qty_rejected` is the damaged/refused portion of `qty_received`. Only the **accepted** quantity (`received − rejected`) increments `purchase_order_item.qty_received` and enters stock, so the line stays outstanding for the rejected units and a replacement shipment can be received later.

**AD-7 — A new *generic* inventory ledger.** Before this module, `inventory_slab_ledger` covered serialized slabs only, and plain `inventory_stock.quantity_on_hand` had no audit trail and no writer outside `fabrication/`. `inventory_ledger` fills that gap for non-serialized stock, deliberately mirroring the slab ledger's discipline, including its best idea: **partial unique indexes on `(source_line_id)` per event make double-posting unrepresentable rather than merely tested against.**

Invariant, identical to the slab ledger's:
```
inventory_stock.quantity_on_hand = SUM(inventory_ledger.quantity_delta)
                                   per (inventory_item_id, warehouse_id)
```

`source_record_id`/`source_line_id` are intentionally FK-free — the ledger is polymorphic over source documents and a real FK cannot point at more than one table.

**AD-8 — The rollup is applied by `purchaseorder`, in the caller's transaction.** Two facts forced this:
1. `purchaseorder.Transition` begins its **own** transaction and row-locks the order, so a posting cannot call it from inside its own transaction without deadlocking against itself.
2. The user-facing PO transition map is one-way — `RCVD→PART` and `PART→SENT` are forbidden — so a **void could never roll the header back**.

So `purchaseorder.ApplyReceiptRollup(ctx, tx, poID, actor)` was added, carrying its own bidirectional `rollupTransitions` map covering only the three receiving statuses. The user-facing map is untouched: a *person* still may not walk an order backwards; returned goods may. `DRFT`/`PAPV`/`APPV` are unreachable and `CLSD`/`CANC` never reopen, so a late or reversed receipt cannot resurrect a closed order.

**AD-9 — Numbering is `IRCT-000001`,** generated post-insert from the SERIAL id like every sibling.

---

## 3. Schema (4 new tables + 1 constraint relaxation)

- **`item_receipt`** — v2 header: hybrid `SERIAL`+`UUID` PK, `IRCT-` number, `record_type`=IRCT, status FK, `purchase_order_id` FK, vendor id + name snapshot, `warehouse_id`, shipping paperwork (packing slip / carrier / tracking / BOL), `item_receipt_owner_id` (IDOR scope owner), posted/voided pairs with CHECKs, `over_receipt_reason`, `custom_fields JSONB`, audit tail + paired soft delete + `record_version`.
- **`item_receipt_line`** — FK to `item_receipt` (`ON DELETE CASCADE`) and to `purchase_order_item` (**NOT NULL**), snapshot sku/name/description/unit, `qty_received`, `qty_rejected` (`<= qty_received`), `line_notes`.
- **`item_receipt_history`** — structural mirror of `purchase_order_history`; actions `create|transition|update|post|void`.
- **`inventory_ledger`** — AD-7, with `uq_inventory_ledger_receipt_line` and `uq_inventory_ledger_return_line`.
- **Relaxation of `chk_poi_qty_received`** (AD-3) — the only non-additive change. Handled as two coordinated edits so fresh and already-provisioned databases converge: the `CREATE TABLE` block now carries `CHECK (qty_received >= 0)` under the name `chk_poi_qty_received_nonneg`, and a guarded `DO $$` stanza drops the old constraint and adds the new one on databases provisioned by PR #76. It is a **relaxation** — every row satisfying the old constraint satisfies the new one, so it cannot fail on existing data and drops nothing.

Indexes: partial live-row on `(purchase_order_id)`, `(vendor_id)`, `(status)`, `(warehouse_id)`, `(owner_id)`, `(date)`; keyset pairs `(created_at, id)` and `(updated_at, id)`; GIN on custom fields; line indexes on `(item_receipt_id)`, `(purchase_order_item_id)`, `(inventory_item_id)` plus a partial unique `(item_receipt_id, line_number)`; ledger indexes on `(item, warehouse)` and `(source_record_type, source_record_id)`.

**Seeds: none.**

---

## 4. API surface

All under `tenantChain`, with the `controllers/purchaseorder.go` auth skeleton copied verbatim.

| Route | Handler | RBAC |
|---|---|---|
| `GET /api/tenant/item-receipts` | List (cursor) | `item_receipt:read` + scope |
| `POST /api/tenant/item-receipts/search` | Search (`query/`) | `item_receipt:read` + scope |
| `POST /api/tenant/item-receipts` | Create | `item_receipt:create` |
| `GET /api/tenant/item-receipts/{uuid}` | Get (+ lines) | `item_receipt:read` + IDOR |
| `PATCH /api/tenant/item-receipts/{uuid}` | Update (PEND only) | `item_receipt:update` + IDOR |
| `DELETE /api/tenant/item-receipts/{uuid}` | Soft delete (PEND/VOID only) | `item_receipt:delete` + IDOR |
| `POST /api/tenant/item-receipts/{uuid}/post` | Post | `item_receipt:transition` + IDOR (+ `:approve` past tolerance) |
| `POST /api/tenant/item-receipts/{uuid}/void` | Void + reverse | `item_receipt:transition` + IDOR |
| `POST /api/tenant/item-receipts/{uuid}/transition` | Status change | `item_receipt:transition` + IDOR |
| `GET /api/tenant/item-receipts/{uuid}/audit` | Audit trail | `item_receipt:read` + IDOR |
| `GET /api/tenant/purchase-orders/{uuid}/receipts` | Receipts for an order | `purchase_order:read` + IDOR **on the order** |

`ErrOverReceipt` maps to **403**, not 400: the request is well-formed and the quantities are real; the caller simply is not permitted to accept the over-delivery. It is logged as `over_receipt_denied`.

Filter whitelist: `id`, `document_number`/`record_number`, `purchase_order_id`, `purchase_order_number`, `vendor_id`, `vendor_name`, `status`, `warehouse_id`, `owner_id`, `receipt_date`, `packing_slip`, `carrier`, `tracking_number`, `created_by`, `updated_by`, `created_at`, `updated_at`, plus `cf:<key>`. Sortable (stable, non-null only): `record_number`, `document_number`, `receipt_date`, `status`, `warehouse_id`. `posted_at`/`voided_at` are deliberately excluded — nullable columns break keyset-cursor comparison.

`resourceForKey` in `controllers/crm.go` is **not** touched; the seeded `item_receipt` workflow exists solely as the custom-field definition host.

### RBAC roles

`warehouse_agent` and `purchase_manager` are **not code** — StoneSuite RBAC is dynamic and only `super_admin`/`guest` are immutable system roles. One catalog entry was added, `{ResourceItemReceipt, ActionApprove}`, following the `creditmemo`/`refund` precedent where the *authorizing* action is split from ordinary record movement. Intended tenant configuration:

- **`warehouse_agent`** — `item_receipt:{create,read,update,transition}` scope `own`; `purchase_order:read` scope `all`; `inventory_item:read`.
- **`purchase_manager`** — the above at scope `all`, plus `item_receipt:{delete,approve}` and `purchase_order:transition`.

---

## 5. Package layout (`itemreceipt/`, every file ≤300 lines)

`types.go`, `numbering.go`, `transitions.go`, `tolerance.go`, `resolver.go`, `inventory_post.go`, `store.go`, `store_get.go`, `store_create.go`, `store_update.go`, `store_search.go`, `store_post.go`, `store_void.go`, `store_transition.go`, plus table-driven tests per pure file and a `//go:build dbtest` round-trip. Controllers: `controllers/itemreceipt.go`, `controllers/itemreceipt_actions.go`, `controllers/itemreceipt_audit.go`. Added to `purchaseorder/`: `receipt_apply.go` + test.

**Reused, not rewritten:** `purchaseorder.RollupReceiptStatus`, `controllers.recordInScope`, `workflow.ValidateCustomFieldsPartial`, `workflow.LogAuditFull`, the `query/` engine, and `fabrication/allocation.go`'s ledger-and-stock shape.

---

## 6. Two traps worth recording

**PostgreSQL evaluates CHECK constraints on the proposed insert row before detecting an `ON CONFLICT`.** A single `INSERT ... ON CONFLICT DO UPDATE` for the stock delta therefore dies on `chk_inventory_stock_on_hand` whenever the delta is negative — it never reaches the UPDATE branch. Stock movement must be **UPDATE first, INSERT only when no row exists**, which is exactly why `fabrication/allocation.go:229` is written that way. The dbtest round-trip caught this; unit tests could not have.

**`CreatePurchaseOrderInput` embeds an unexported field struct,** so no other package can populate its line items. Cross-module test fixtures seed purchase orders with direct SQL instead.

---

## Deferred (hooks left ready)

- **Serialized slab intake** — `inventory_slab` already carries `slab_received_at`, `slab_received_by`, `slab_vendor_id`, `slab_supplier_packing_ref`: receiving-shaped columns with no flow behind them. `item_receipt_line.inventory_item_id` and the `inventory_ledger.source_*` triple are the attachment points.
- **Vendor Bill 3-way match** (PO ⨯ Receipt ⨯ Bill) — needs stable `item_receipt_line_id`, which this module provides.
- **Per-tenant over-receipt tolerance** — `tolerance.go` is the single seam.
- **Returns to vendor** — the `'returned'` ledger event code is written by Void today; a standalone RTV document would reuse it.

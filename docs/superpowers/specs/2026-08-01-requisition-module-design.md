# Requisition Module — Backend Design Spec

**Date:** 2026-08-01
**Status:** Draft — proceeding to implementation.
**Scope:** New Requisition module (header + line items + approval gate + conversion into a Purchase Order) for StoneSuite's Purchases section. A structural twin of `purchaseorder/`, one step upstream of it in the procurement chain: `requisition → purchase_order → item_receipt → vendor_bill`.

---

## 1. Overview & Goals

Add a Requisition module — an internal request-to-buy, raised before a vendor or price is finalized, that a controller approves and then converts into a real Purchase Order. It follows the same v2 relational conventions as every sibling document module: hybrid PK (`SERIAL` + `UUID`), `employee`-based audit columns, paired soft delete, `record_version`, the RBAC/scope/IDOR chain, the `query/` filter engine, keyset pagination, and the AD-8 configuration-driven approval gate.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Requisition record type | `lkp_record_type` row `REQN` / `requisition` (**id 12**) — seeded, unused until now | `tenant/schema.sql:705` |
| Requisition status lifecycle | `lkp_record_status` rows for `record_type=12`: `DRFT, PAPV ("Pending Approval"), APPV ("Approved"), CANC` — seeded, unused until now | `tenant/schema.sql:745` |
| RBAC resource + 5 actions | `authz.ResourceRequisition` (create/read/update/delete/transition) — seeded, unused until now | `authz/catalog.go:47,284-288` |
| Legacy v1 JSONB collision | A `workflows` row keyed `"requisition"` already exists in the old JSONB CRM engine (schema.sql ~1889-1928, routed via `controllers/crm.go`). Same precedent as `vendor`/`sales_order`: **left untouched**, this module lives in its own tables/package. | `controllers/crm.go` |
| Vendor (suggested counterparty) | `vendor` table + `vendors/` module | `tenant/schema.sql:3425` |
| SKU line source | `inventory_item` | `tenant/schema.sql:2376` |
| Lookups | `lkp_unit`, `lkp_payment_terms` | `tenant/schema.sql` |
| Approval pattern (AD-8) | `purchase_order_approver`/`purchase_order_approval` + `purchaseorder/approval.go` | `purchaseorder/` |
| Conversion-lineage pattern | `quote_conversion` (quote → sales_order) — direct-insert-in-one-transaction pattern, no cross-package import of the source module | `tenant/schema.sql:3226`, `salesorder/store_convert.go` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Money calc pattern | `purchaseorder/calc.go` (`ComputeLine`/`ComputeHeader`, `round2`) — simplified here (no per-line discount/tax; see AD-3) | `purchaseorder/` |
| Audit log | `audit_logs` via `workflow.LogAuditFull` | `workflow/` |
| Row-level IDOR guard | `recordInScope(ctx, pool, scope, identityID, ownerUserID)` | `controllers/scope.go:29` |
| Auth skeleton (the correct one) | `controllers/purchaseorder.go` — logs `permission_denied`, has the full approve/transition/IDOR shape already | `controllers/purchaseorder.go` |

**Key finding that shaped this design:** exactly like Purchase Order before it, Requisition was already half-scaffolded — record type 12, its four lifecycle statuses (`DRFT/PAPV/APPV/CANC`), and the full RBAC catalog rows exist and are unused. Both are adopted **as-is**; this module adds zero new `lkp_record_type`/`lkp_record_status` seed rows.

### What is genuinely missing (new tables — justified in §3)

- `requisition`, `requisition_item`, `requisition_history`, `requisition_approver`, `requisition_approval`, `requisition_conversion` — no existing table can represent a multi-line, approvable internal purchase request with a lineage into a Purchase Order.
- No new lookup tables. No changes to any existing table (`purchase_order`/`purchase_order_item` are only read from, in the conversion path).

---

## 2. Architecture Decisions

**AD-1 — Relational, purchaseorder-shaped, not JSONB.** Same reasoning as Purchase Order: stable line ids, FK to `inventory_item`, an approval gate — none of which the JSONB engine offers. Custom fields still ride in `requisition_custom_fields JSONB`, validated against a `"requisition"` **relational-module** workflow key (distinct from the pre-existing legacy JSONB `"requisition"` CRM workflow — same key collision precedent already accepted for `vendor`/`sales_order`; `workflow.GetWorkflowByKey` returning not-found is treated as "no custom fields configured", exactly as `purchaseorder.validateCustom` does).

**AD-2 — Vendor is a suggestion, not a commitment.** `requisition_vendor_id` is **nullable** (unlike PO's mandatory vendor) — a requisition can be raised before anyone has decided who to buy from. When set, the display name is snapshotted into `requisition_vendor_name` at create/update time, same never-re-derive rule as every sibling snapshot.

**AD-3 — Lines are SKU or free-form; no per-line discount/tax.** A line optionally carries `inventory_item_id` (nullable — a requisition can ask for something not yet in the catalog). Money is deliberately simpler than PO's: `estimated_amount = quantity × estimated_unit_price` per line (no discount, no per-line tax rate) — a requisition is a rough ask, not a priced commitment. The header applies one flat `requisition_sales_tax_percent` on top of the line subtotal, mirroring PO's header-level tax field.

**AD-4 — No address/ship-to block.** A requisition is an internal document; it never leaves the tenant, so unlike PO (which snapshots a ship-to for the vendor) it carries none. The eventual Purchase Order's ship-to is filled in at conversion/PO-creation time.

**AD-5 — Requester is the IDOR scope owner.** `requisition_requested_by_id` (`employee`, NOT NULL, defaults to the creating actor) doubles as the scope-owner column — the same role `purchase_order_owner_id` plays for PO — rather than adding a second redundant column. "Own" scope narrows to requisitions the caller requested.

**AD-6 — Status machine (adopting the seeded codes verbatim, mirrors PO's first three states before `SENT`):**

```
DRFT ──Submit──▶ PAPV ──Approve (AD-8 gate)──▶ APPV
 │  ▲              │                             │
 │  └──Recall──────┘ (PAPV→DRFT, rework)         └─Revise──▶ DRFT
 └────────────▶ CANC   (also PAPV→CANC, APPV→CANC)
```

- `CANC` is terminal. There is no `RJCT` status seeded for REQN (same as PO) — rework is `PAPV → DRFT`.
- Conversion to a Purchase Order (§4) is **orthogonal to status**, exactly like quote → sales order: it does not itself move `requisition_status`. A requisition should typically be `APPV` before conversion, but that is enforced as a business rule in `ConvertToPurchaseOrder` (`ClientError`), not by a status transition.

**AD-7 — Approval is the AD-8 configuration-driven gate, exact structural copy of `purchase_order_approver`/`purchase_order_approval`.** Approvers are configured per `(record_type=REQN, status=PAPV)`; zero configured approvers means the gate is open. `Approve` enforces `authz.ActionTransition`, matching the documented convention (`ActionApprove` is not granted to document modules; `ResourceRequisition`'s catalog rows already reflect this — 5 rows, no `ActionApprove`).

**AD-8 — Conversion into a Purchase Order (`requisition_conversion`), destination-owns-the-write pattern.** `purchaseorder.ConvertFromRequisition` lives in the **`purchaseorder`** package, not `requisition` — the same convention `salesorder.ConvertFromQuote`/`invoice.ConvertFromSalesOrder` already established: the package that owns the destination table performs the write (one transaction, raw SQL against the source's `requisition`/`requisition_item` tables, no import of the `requisition` Go package). The HTTP route still lives on the source controller (`POST /api/tenant/requisitions/{uuid}/convert`, mirrors `controllers/salesorder_convert.go`'s `SalesOrderOps.Convert` calling into `invoice.ConvertFromSalesOrder`), checking `requisition:read` on the source (IDOR-guarded) and `purchase_order:create` on the target inline. Every requisition line is copied verbatim into a new PO line (no re-pricing); the caller **must** supply a vendor UUID for the new PO (a requisition's vendor is only ever a suggestion — AD-2 — so it cannot silently become the PO's mandatory vendor; the suggested vendor is offered by the UI as a pre-filled default, decided by the caller, not by the store layer). Only an `APPV` requisition may convert. Lineage is recorded in `requisition_conversion` (`requisition_id`, `purchase_order_id` UNIQUE, `converted_at/by`, a `{requisitionItemUuid: purchaseOrderItemUuid}` snapshot). A requisition converts **at most once** (`uq_requisition_conversion_requisition`); replaying the call after a successful conversion returns the existing PO and `created=false`, matching the idempotent-retry contract of `ConvertFromQuote`.

**AD-9 — Money math is `purchaseorder/calc.go`'s shape, simplified per AD-3:** per line `qty × unitPrice` rounded to 2dp (no discount/tax term); header sums line amounts into a subtotal, applies `requisition_sales_tax_percent`, no shipping/adjustment terms (those belong to the PO, not the ask).

**AD-10 — Numbering is `REQN-000001`**, generated post-insert from the SERIAL id (`FormatNumber`), same convention as every sibling.

**AD-11 — Delete is soft, DRFT/CANC only.** Mirrors PO's AD-9 guard — once submitted for approval the request is visible to an approver and keeps its trail.

**AD-12 — Update is DRFT-only.** Mirrors PO's AD-10 — the whole document is editable only in `DRFT`; rework flows through the recall transitions.

**AD-13 — Attachments are out of scope.** No relational document module (`purchaseorder`, `itemreceipt`) has file-attachment support yet; this module does not invent one either. Deferred to a future, cross-module increment.

---

## 3. Schema (6 new tables, all idempotent, appended at the end of `tenant/schema.sql`)

`requisition` (header, no ship-to block — AD-4), `requisition_item` (lines, simplified money — AD-3), `requisition_history` (status/action trail, mirrors `purchase_order_history`), `requisition_approver`/`requisition_approval` (AD-7, exact structural copies of `purchase_order_approver`/`purchase_order_approval`), `requisition_conversion` (AD-8, mirrors `quote_conversion`). Column-level detail lives in the migration itself, not duplicated here — see `database/migrations/tenant/schema.sql`, "REQUISITION MODULE" section.

Zero new `lkp_record_type`/`lkp_record_status` seed rows (§ "What already exists"). Zero changes to any existing table.

---

## 4. Package Layout (`requisition/`)

Mirrors `purchaseorder/`'s file anatomy: `types.go`, `calc.go`, `numbering.go`, `transitions.go`, `resolver.go`, `approval.go`, `store.go` (shared helpers), `store_create.go`, `store_update.go`, `store_search.go`, `store_transition.go`, `store_get.go`, plus table-driven tests for every pure file and a `//go:build dbtest` store test. No `store_convert.go` here — AD-8 puts that file in `purchaseorder/` instead (`purchaseorder/store_convert.go`, `ConvertFromRequisition`).

## 5. Controller (`controllers/requisition.go` + `requisition_audit.go`)

Copied from `controllers/purchaseorder.go` (the corrected auth skeleton — logs `permission_denied`/`idor_denied`, 404-not-403 on scope denial). Routes:

```
GET    /api/tenant/requisitions                    — list (cursor-paginated)
POST   /api/tenant/requisitions/search              — filter + sort + search + pagination
POST   /api/tenant/requisitions                     — create
GET    /api/tenant/requisitions/{uuid}              — get (+ items)
PATCH  /api/tenant/requisitions/{uuid}               — update (DRFT only)
DELETE /api/tenant/requisitions/{uuid}               — soft delete (DRFT/CANC only)
POST   /api/tenant/requisitions/{uuid}/transition    — status change
POST   /api/tenant/requisitions/{uuid}/approve       — approval sign-off
POST   /api/tenant/requisitions/{uuid}/convert       — convert to Purchase Order (AD-8), body {"vendorUuid": "..."}
GET    /api/tenant/requisitions/{uuid}/audit         — audit trail
```

`Convert` requires `authz.ActionTransition` (the requisition is being moved out of its "open for editing" life stage), same convention as `Approve`.

## 6. RBAC / Generic Router

No changes needed. `authz.ResourceRequisition` already carries the 5 catalog rows (create/read/update/delete/transition) at `authz/catalog.go:284-288`. `controllers/crm.go`'s `resourceForKey` already maps the legacy JSONB `"requisition"` key to `authz.ResourceRequisition` for the old engine — untouched, unrelated to this relational module (AD-1).

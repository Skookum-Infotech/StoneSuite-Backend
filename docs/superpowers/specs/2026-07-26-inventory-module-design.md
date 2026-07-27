# Inventory Management Module — Backend Design Spec

**Date:** 2026-07-26
**Status:** Approved, not yet implemented. Branch `feat/inventory-module` (cut from `feat/chart-of-accounts`).
**Scope:** Warehouse/bin locations, stone attributes on the item catalogue, and the generalisation of `inventory_slab` into a general serialized inventory unit. Phase 1 (schema) and Phase 2 (CRUD) only. **Warehouse transfer, stock adjustment and cycle count are explicitly out of scope** and become a separate spec (AD-6).

---

## 1. Overview & Goals

Add **Inventory Management** — the tenant's physical stock: what a product *is* (catalogue), what individual pieces exist (serialized units), where they physically sit (bins), and an immutable record of how they moved (ledger + history).

### What already exists (reuse, do not recreate)

**This module is ~65% built already, scattered across three packages.** A spec that treated it as greenfield would produce a second, competing inventory system beside the working one. This is the single most important fact shaping this design.

| Asset | State | Location |
|---|---|---|
| `inventory_item` catalogue | **Exists** — full CRUD, 6 routes, 4 RBAC rows, `query/` resolver | `schema.sql:2376`, `inventory/` (4 files, 403L) |
| `inventory_slab` physical unit | **Exists** — dimensions, area, lot/bundle/block, offcut lineage, status | `schema.sql:4196`, Go code in `fabrication/slab_store.go` |
| `inventory_stock` on-hand | **Exists** — `UNIQUE(item, warehouse)`, `CHECK (quantity_on_hand >= 0)` | `schema.sql:2409` |
| `inventory_ledger` (bulk) | **Exists** — append-only, polymorphic source | `schema.sql:4821` |
| `inventory_slab_ledger` | **Exists** — append-only, once-only partial indexes | `schema.sql:4261` |
| `inventory_allocation` | **Exists** — SO-only reservations | `schema.sql:2579` |
| `lkp_unit` / `lkp_warehouse` / `lkp_tax_rate` | **Exist and seeded** — but have **no API at all** | `schema.sql:2299 / 2323 / 2354` |
| `query/` filter engine, `secret.Cipher`, `authz` | **Exist** — reused | — |
| Bin / location / rack / yard table | **None.** Genuinely new | — |
| `'adjusted'` ledger event | **Legal on both ledgers, written by nothing** | `schema.sql:4275, 4832` |

The requested `schema.org/Product` → `schema.org/IndividualProduct` split **already exists** as `inventory_item` → `inventory_slab`. This spec extends that split; it does not re-model it.

**Two gaps worth naming up front.** There is no HTTP endpoint for units, warehouses or tax rates anywhere in the app — `inventory_item_unit_id` is `NOT NULL` (`schema.sql:2382`) yet a frontend item form cannot populate its dropdown. And `controllers/inventory.go:56` returns 403 without `logSecurityEvent`, violating CLAUDE.md RBAC rule 6; every sibling module logs it.

**Non-negotiable constraints (CLAUDE.md):** database-per-tenant, no `tenant_id` columns; idempotent append-only schema; the mandatory `/api/tenant/` chain (JWT + `TenantResolver` + `authz.Check`) with `permission_denied` logging; all list/search through `query/`; files ≤300 lines; table-driven tests for pure functions.

**Single-entity policy (2026-07-25):** one subsidiary, one company, one country. No `company_id` or per-row `currency_id` appears here.

---

## 2. Architecture Decisions

**AD-1 — Bins are one flat table with a `bin_type` and an optional self-parent, not a fixed zone/aisle/rack/shelf hierarchy.** A stone yard's topology is not uniform-depth: outdoor granite lives `YARD-A → AF-03 → SLOT-7` (3 levels), indoor quartz `RACK-12 → SHELF-B` (2), receiving is a single `STAGING` area (1). Four fixed tables force synthetic filler rows for every missing level — invisible in the UI, breeding duplicates, and every query must know which levels are real. Flat also gives `inventory_slab` one honest FK instead of four nullable ones with an unenforceable "exactly the deepest is set" CHECK. Cost: ancestor queries would need `WITH RECURSIVE` — mitigated by a materialized `bin_path` so "where is this slab?" is one column read. Cycles become representable — mitigated by `chk_bin_not_self`, an explicit walk-up in the store, and a depth cap of 4. Precedent in-file: `lkp_coa_subcategory → lkp_coa_category` (`:4928`) and `workflow_records.parent_record_id` (`:452`).

**AD-2 — Bins locate serialized units only. `inventory_stock` is NOT re-keyed.** It stays `UNIQUE(inventory_item_id, warehouse_id)`. The consequence is the load-bearing one: **a bin move is stock-neutral by construction and therefore writes no ledger row at all** — it is one `inventory_unit_history` row. This keeps `itemreceipt/inventory_post.go`, `fabrication/allocation.go` and `salesorder/allocation.go` completely untouched, and it collapses "bin transfer" from an anticipated Phase 3 document module into a Phase 2 `PATCH`.

**AD-3 — Stone attributes are typed columns + `lkp_*` tables, not `custom_fields` JSONB.** The JSONB column exists (`:2386`) with a GIN index and the resolver already exposes `cf:<key>`. It is still wrong here: JSONB values are untyped text, so `thickness_mm < 25` cannot be a numeric comparison; there is no FK, so a typo silently creates a phantom colour; and the item picker cannot `JOIN lkp_color` for a swatch. Material, colour, finish and thickness are queried on *every* item search in a stone business.

**AD-4 — `lkp_edge_profile` is not created.** An edge profile is a property of a fabricated countertop, not of a slab sitting in the yard. It belongs on a quote/estimate line or `fabrication_job_item`. Creating an unreferenced table here would be drift — a table no test exercises and no code reads.

**AD-5 — A bundle gets its own table; it is not a `unit_kind` on `inventory_slab`.** A bundle is a *container* of 8–12 slabs with no area of its own, whose members are already counted. Modelled as an `inventory_slab` row it would (i) have to satisfy `chk_slab_dims` and `chk_slab_area` (`:4243`), which demand `length/width/thickness > 0` and `area > 0` on a thing with no dimensions — forcing fake numbers; and (ii) require **every** stock, area and valuation query to remember `AND slab_unit_kind <> 'bundle'`. Six such queries exist today. One forgotten predicate silently doubles the on-hand area of the entire yard, `inventory_stock` has no cross-check that would catch it, and the failure is both invisible and financial. Unrepresentable-by-construction is the house style (`:4253`, `:4835`).

**AD-6 — Phase 3 document tables are deferred, not pre-created.** The instinct is to create adjustment/transfer/count headers and lines now to avoid a later migration. That instinct rests on a false premise (AD-7): appending tables later costs nothing. And deferring is actively better — each is a document module needing header + line + history + approver + approval (15 tables) plus numbering and transition rows. Guessing 15 tables against no spec and no written approval rules produces 15 tables Phase 3 works around. Empty speculative tables also drift silently: no test exercises them, so nothing catches the mistake for months. **Created now** only where Phase 1/2 code actually references them: `lkp_inventory_reason`, the three record types with their statuses, and `inventory_unit_history`.

**AD-7 — Columns are added with `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, and the existing `CREATE TABLE` bodies are never edited.** `CLAUDE.md:167` claims tenant migrations never use `ALTER TABLE` to add columns. That is **stale and false**: the schema file's own header lists `ADD COLUMN IF NOT EXISTS` as an idempotency mechanism (`:6`), `.claude/skills/add-migration/SKILL.md:28` prescribes it verbatim, and the file already does it 17 times. The rule predates the removal of numbered migrations, which makes its remedy ("use migrations instead") incoherent. Corrected in a separate `docs:` commit. Two real constraints do apply: **never edit an existing `CREATE TABLE` body** — `CREATE TABLE IF NOT EXISTS` is a no-op on existing tenants, so a column added there reaches only fresh databases and diverges permanently; and **`ADD CONSTRAINT` is not idempotent** — it errors on the second boot and breaks every tenant, so new CHECKs need a `DO $$` existence guard (precedent `:4877`).

**AD-8 — `inventory_item_tracking` discriminates serialized from quantity items.** Not in the original brief, and the highest-value column in it. Today nothing on `inventory_item` says whether an item is slab-tracked or quantity-tracked, yet `inventory_slab_ledger` and `inventory_ledger` **both** drive the same `inventory_stock` row. Nothing stops an item receiving stock through both paths and double-counting. Defaults to `'quantity'`, correct for every existing row.

**AD-9 — `slab_finish_id` is authoritative; `slab_finish` is dual-written.** The existing `slab_finish VARCHAR(50)` (`:4222`) cannot be dropped. Both columns therefore exist forever, and if any writer sets only one, item search by finish silently misses rows. Rule: writes set **both**; reads prefer the id and fall back to the string when NULL.

**AD-10 — The `inventory_item` → `inventory_unit` permission split ships with an idempotent backfill.** Moving slab routes to a new `inventory_unit:*` resource would silently 403 every custom tenant role holding `inventory_item:*` — a 403 storm on deploy, invisible until a user complains. `super_admin` is unaffected (it holds a wildcard `('*','*','all')`). An `INSERT ... SELECT ... ON CONFLICT DO NOTHING` in `schema.sql` grants the new permission to every role that already has the old one, self-healing on the next boot of every tenant.

**AD-11 — The `inventory_ledger` once-only indexes are re-keyed on `(source_record_type, source_line_id)`.** `uq_inventory_ledger_receipt_line` and `_return_line` (`:4835`) key on `source_line_id` **alone**, while the table is explicitly polymorphic over source documents (`:4816`) with `source_record_type` as the discriminator. Failure: `item_receipt_line` id 4211 posts `received`; later a Phase 3 `inventory_adjustment_line` gets id 4211 from an independent SERIAL — collision is near-certain, not a corner case — and its insert trips the index. `itemreceipt/inventory_post.go:44` maps unique violations to `ErrMovementAlreadyApplied`, so the user is told a document they never posted was already applied, **and stock is silently never incremented**. The repair is a relaxation: every pair rejected by the new index was rejected by the old one, so it cannot fail on existing data.

---

## 3. Schema

One new section appended at EOF of `database/migrations/tenant/schema.sql` (5268 lines), using the Chart of Accounts banner style (`:4908`). **FK order within the section is load-bearing:** lookups → `inventory_bin` → `inventory_bundle` → ALTERs → history → ledger repair → record-type seeds → RBAC backfill.

### 3.1 Vocabulary lookups

All follow the `lkp_unit` shape (`:2299`): `SERIAL` pk, code `UNIQUE`, `is_active`/`is_system`, full audit columns, soft-delete CHECK.

| Table | Seeds |
|---|---|
| `lkp_material` | 12 — Granite, Marble, Quartz (Engineered), Quartzite, Soapstone, Porcelain, Sintered Stone, Dolomite, Onyx, Travertine, Limestone, Slate. Carries `material_is_porous`, which drives the sealing step |
| `lkp_color` | **Deliberately empty.** Colour names are vendor catalogue names; a guessed seed set collides with the tenant's real import and leaves dead rows no partial index can distinguish from live ones. Carries `color_hex` for a swatch and an optional `color_material_id` |
| `lkp_finish` | 8 — Polished, Honed, Leathered, Brushed, Flamed, Sandblasted, Antiqued, Sawn/Raw |
| `lkp_inventory_reason` | 10 — Damage, Breakage, Theft, Shrinkage, Scrap, Found, Recount, Cycle Count Variance, Warehouse Transfer, Data Entry Correction. Carries `applies_to` (adjustment\|transfer\|count\|scrap\|any), `direction` (increase\|decrease\|both) and an optional `coa_account_id` for future GL posting |

### 3.2 `inventory_bin`

Per AD-1: `warehouse_id` FK, `bin_code`, `bin_type` ∈ `yard|rack|aframe|aisle|shelf|floor|staging`, `bin_parent_id` self-FK, materialized `bin_path` (`'YARD-A/AF-03/SLOT-7'`) and `bin_depth` (CHECK 0–4), advisory `bin_capacity_units` / `bin_capacity_area`, `bin_notes`, full audit columns.

`bin_code` is unique **per warehouse among live rows only**, matching `uq_inventory_item_sku_active` — a code frees up on soft delete. Indexes: warehouse+active, parent, `bin_path varchar_pattern_ops` (prefix subtree search), and the two keyset-cursor pairs `(created_at, id)` / `(updated_at, id)` that `query/` needs.

**One seeded row:** a `STAGING` bin in `MAIN`, so receiving has a default destination. It resolves the warehouse **by subselect on `warehouse_code`** and uses `WHERE NOT EXISTS`, **not** `ON CONFLICT` — the uniqueness is a *partial* index, which cannot be named as a conflict target.

The `NOT EXISTS` guard is scoped to **live rows only**, matching the partial index exactly. Scoping it to all rows instead would mean soft-deleting this system bin leaves the tenant permanently without a staging destination: the guard keeps finding the dead row and skips the insert on every subsequent boot. Matching the index scope makes a deleted system row reappear on the next boot — the same behaviour the seeded chart of accounts already has via `uq_coa_account_code_live` (`:5041`) plus a targetless `ON CONFLICT DO NOTHING`. Phase 2's bin delete path must *additionally* refuse to soft-delete a `bin_is_system` row, so resurrection stays a backstop rather than the normal path.

### 3.3 `inventory_bundle`

Per AD-5. `bundle_code` (unique among live rows), vendor, supplier code, block/lot, optional item, `warehouse_id`, optional `inventory_bin_id`, and `bundle_status` ∈ `open|sealed|broken`. A bundle has no area and **never** appears in `inventory_slab_ledger`; only its member slabs do. Supersedes the free-text `inventory_slab.slab_bundle_id` (`:4216`), which is retained for historical rows and back-filled from `bundle_code` on write.

### 3.4 `inventory_item` extension

8 `ADD COLUMN IF NOT EXISTS`: `tracking` (AD-8), `material_id`, `color_id`, `finish_id`, `thickness_mm`, `origin_country_id`, `barcode`, `default_warehouse_id`. Two `DO $$`-guarded CHECKs (`tracking IN ('quantity','serialized')`, `thickness_mm >= 0`). Six indexes including a partial unique on non-empty barcode and the two keyset pairs the resolver implies but the schema never got.

### 3.5 `inventory_slab` extension

9 `ADD COLUMN IF NOT EXISTS`: `slab_unit_kind` (`slab|remnant`), `inventory_bin_id`, `slab_barcode`, `slab_finish_id` (AD-9), `inventory_bundle_id`, `slab_sequence_in_bundle`, `slab_is_usable_remnant`, `slab_remnant_reason_id`, `slab_root_slab_id`.

`slab_root_slab_id` is a denormalised root ancestor: recall ("every piece descending from vendor lot X") becomes one indexed equality instead of a `WITH RECURSIVE` over `slab_parent_slab_id`. Three `DO $$`-guarded CHECKs, including `slab_unit_kind <> 'remnant' OR slab_form = 'cut'` — a remnant is by definition a cut piece. `idx_slab_remnant_pick` is a partial index on `(inventory_item_id, slab_area DESC)` so the remnant picker is one index scan.

### 3.6 History tables

`inventory_item_history` and `inventory_unit_history`, both copying `coa_account_history`'s field-level shape (`:5053`). Neither table has history today while every sibling does.

`inventory_unit_history` is **distinct from `inventory_slab_ledger` on purpose.** The ledger is the *financial* record — signed quantity deltas that must sum to `inventory_stock`, with partial unique indexes making each stock event once-only. The history is the *operational* record — bin moves, re-grades, photo swaps, cut events — none of which change on-hand quantity and none of which may therefore touch the ledger (AD-2). Writing a bin move to the ledger with delta 0 would collide with the once-only indexes and pollute the audit trail with non-events. It carries `from_bin_id`/`to_bin_id`, `from_warehouse_id`/`to_warehouse_id` and an optional `inventory_reason_id`.

### 3.7 Ledger index repair

Per AD-11, keyed on `(COALESCE(source_record_type, 0), source_line_id)`. `COALESCE(...,0)` rather than a `NOT NULL` predicate, because NULLs are DISTINCT in a unique index — a NULL record type would silently drop the guarantee for exactly those rows. Kept as **two** indexes rather than one on `(event, type, line)` to preserve the existing semantics exactly: a line may be received once *and* returned once.

**The corrected definition replaces the original statement in place (`:4838`); the appended section only drops the two legacy index names.** This split is load-bearing and was found the hard way. The obvious structure — leave `:4838` alone and drop/recreate at EOF — is broken: the appended `DROP` runs after the upstream `CREATE`, so on the *next* boot `CREATE UNIQUE INDEX IF NOT EXISTS` at `:4838` no longer short-circuits and rebuilds the **old** index, which the appended stanza then drops again. That churn is invisible on an empty table and **fatal** the moment a tenant holds two legitimately-colliding rows: the upstream `CREATE` fails, the single-transaction apply aborts, and **the tenant can no longer boot at all**.

A fresh-database apply cannot surface this — three consecutive clean applies passed before the bug was found. It took inserting two realistic colliding rows (an `item_receipt_line` and an `inventory_adjustment_line` sharing id 4211) to expose it. Any future in-place index change must follow the same shape: correct the canonical statement, and use the appended section only to retire the old name.

`DROP INDEX` is absent from SKILL.md step 6's forbidden list and loses no data. `DROP INDEX CONCURRENTLY` is unavailable because the whole file applies as one transaction; at current row counts the ACCESS EXCLUSIVE hold is milliseconds.

### 3.8 Record types and RBAC backfill

Three record types — `IADJ`, `ITRF`, `ICNT` — plus their statuses, seeded with the **subselect pattern** from the FJOB block (`:4177`). A literal `record_type_id` would be wrong on any tenant whose lookups were seeded out of order, and `lkp_record_status` keys statuses to types by serial position, so a mis-numbered insert silently mis-assigns every downstream status.

`ITRF` gets `TRNS`/`RCVD` rather than `POST` because a warehouse transfer is genuinely two-legged: stock leaves the source before it arrives, and in-transit must be representable.

Then the AD-10 RBAC backfill.

---

## 4. Package Layout

`inventory/` grows from 4 files to ~30, every one under the 300-line cap, split by verb.

**Moved out of `fabrication/`:** `CreateSlab`/`GetSlab`/`ScrapSlab` and the `Slab`/`CreateSlabInput` types become `inventory.CreateUnit`/`GetUnit`/`ScrapUnit` and `Unit`/`CreateUnitInput`. **`fabrication/slab_store.go` is deleted** — no shim, since a shim leaves `fabrication` owning an inventory concern, which is exactly the coupling this removes. Import graph verified cycle-free: `fabrication` → `inventory` is legal, and `arch_guard_test.go` constrains only `query/` and `ai/`.

**Ledger de-duplication.** `itemreceipt/inventory_post.go:33` and `fabrication/allocation.go:229` hold near-identical private `ledgerAndStock` copies. The canonical version becomes `inventory.LedgerAndStock`; `inventory_post.go` shrinks to a delegating adapter preserving its error mapping. Both hard-won comments move verbatim — in particular why `INSERT ... ON CONFLICT DO UPDATE` **cannot** be collapsed: Postgres evaluates CHECKs on the proposed insert row *before* detecting the conflict, so a negative delta trips `chk_inventory_stock_on_hand` and never reaches the UPDATE branch.

Groups: shared (`errors.go`, `scan.go`), pure logic (`area.go`, `bin_path.go`), item (`store*.go`, `resolver.go`), unit (`unit_store*.go`, `unit_cut.go`, `resolver_unit.go`), bin/bundle/warehouse/lookups, ledger (`ledger.go`, `ledger_slab.go`, `ledger_read.go`).

---

## 5. API Surface

All under `/api/tenant/`, every route on `tenantChain`.

- **Items** — the 6 existing routes kept; bodies gain stone fields. New: `{uuid}/history`, `{uuid}/stock`, `{uuid}/units`.
- **Lookups** — `GET /inventory/lookups` (aggregate: units, warehouses, taxRates, materials, colors, finishes, reasons, binTypes, unitKinds), `GET /inventory/lookups/{kind}` (single vocabulary, paginated, `{kind}` a whitelisted enum never interpolated into SQL), and write routes for the user-extensible vocabularies. **This unblocks the frontend item form and should ship before anything else in Phase 2 that a UI depends on.**
- **Warehouses** — full CRUD plus `{uuid}/set-default`, addressed by `warehouse_uuid`, never the SERIAL.
- **Bins** — list, search, `tree`, CRUD, `{uuid}/units`.
- **Units** — list, search, CRUD, `{uuid}/bin` (bin move — history only, **no ledger row**), `/scrap`, `/cut`, `/history`, and `GET /inventory/units/remnants`. The existing `/inventory/slabs/*` routes stay live so the frontend migrates without a flag day.
- **Bundles** — CRUD, member add/remove, `seal`, `break`.

**Security chain per route**, copying `controllers/payment.go`: (1) `GetUserFromContext` → 401; (2) `PoolFromContext` → 500; (3) `authz.Check`, and on deny `logSecurityEvent(r, "permission_denied", ...)` then 403; (4) single-record GET/PATCH/DELETE gets a `...ByUUID` guard, **denial → 404 not 403**; (5) `*query.InvalidFilterError` → 400, never 500.

Inventory is tenant-global reference data with no owner column, so step 4 is a **uuid-existence** guard rather than an ownership guard — but it must still 404, since the reason (id enumeration) is identical.

---

## 6. RBAC

Five new resources in `authz/catalog.go` — `inventory_unit`, `inventory_bin`, `inventory_bundle`, `warehouse`, `inventory_lookup` — with `{create, read, update, delete}` each (20 rows). **No `ActionTransition`**: none is a status document; Phase 3's three documents will each need transition and approve.

`GET /inventory/lookups` requires `inventory_lookup:read` **only**, not `inventory_item:read` — otherwise a warehouse clerk who may create bins cannot load a warehouse dropdown.

None is a CRM workflow key, so they are **not** added to `resourceForKey` in `controllers/crm.go`; adding them would make `rbac_catalog_drift_test.go` demand the full CRM action set on all five.

---

## 7. Testing

Table-driven, stdlib `testing` for pure functions:

- `area_test.go` — mm→SQFT rounding, mm→SQM, a count-unit item where area is meaningless, unit-category mismatch rejection, offcut yield.
- `bin_path_test.go` — a 4-deep path, a self-cycle, a 3-node cycle, depth-cap overflow, rename-with-children, reparent-with-grandchildren.
- `resolver_test.go` / `resolver_unit_test.go` — every whitelisted key resolves; an unknown key returns `*query.InvalidFilterError`; sortable fields restricted.
- `ledger_test.go` — sign handling, the UPDATE-then-INSERT ordering.

DB-backed tests carry `//go:build dbtest` on line 1 **and** the `TEST_DATABASE_URL` skip guard, matching `payment/store_test.go`.

**The regression gate for the whole `fabrication` → `inventory` move is that `go test ./itemreceipt/...` and `go test ./fabrication/...` pass untouched.**

---

## 8. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| **Unit-of-measure mismatch.** Nothing forces `slab_area_unit_id` = `inventory_item_unit_id`. A SQM slab ledgered against a SQFT item is off by 10.76× and no constraint catches it | High | `area.go` computes area from mm into the *item's* unit and never trusts a client-supplied area; create rejects a mismatch; a count-unit item ledgers `1` per unit. `lkp_unit.unit_category` is the discriminator |
| **Area is not conserved on cut.** Saw kerf (3–5mm/cut) and dropped scrap are real losses | High | Parent's full area `consumed`; each retained offcut `recovered` at its measured area. The shortfall gets **no ledger row** — it is already expressed by consuming more than is recovered, so adding a `scrapped` row would count the kerf twice and drift on-hand down on every cut. It is recorded in `inventory_unit_history` instead. See the worked example on `PlanCut` |
| **Remnant graveyard.** Without a usability threshold the yard accumulates thousands of worthless offcuts showing as available stock | High | `slab_is_usable_remnant` set **at cut time**, never derived on read, so a later threshold change cannot reclassify last year's inventory. Sub-threshold offcuts get a `scrapped` row immediately |
| **`inventory_stock` = `SUM(ledger deltas)` is only a comment.** Two ledgers write the same row; nothing enforces it | High | `ReconcileStock` ships as a read-only report. It **never** auto-corrects — a silent auto-correct destroys the evidence needed to find the drifting writer |
| **RBAC 403 storm on the resource swap** | High | AD-10 backfill |
| **`slab_finish` / `slab_finish_id` divergence** | Medium | AD-9 dual write, with a test |
| **`bin_path` staleness on rename/reparent** | Medium | Subtree rewritten in the same transaction; covered by `bin_path_test.go` |
| **Sealed bundle single-member move** | Medium | Rejected in the store — cross-row, so not expressible as a CHECK |
| **`'adjusted'` stays legal with no writer** | Low now, high in Phase 3 | Not fixable without dropping a CHECK value (forbidden); documented for the Phase 3 spec |

---

## 9. Out of Scope

Warehouse transfer, stock adjustment and cycle count — three document modules, each needing header/line/history/approver/approval tables, numbering and transitions (AD-6).

**Note for that author:** because bins locate serialized units only and `inventory_stock` is keyed `(item, warehouse)` (AD-2), a **bin transfer is stock-neutral** — one history row, no ledger row, no document, no approval. It ships here as `PATCH /inventory/units/{uuid}/bin`. Only *warehouse* transfer, adjustment and count are genuine documents. That removes roughly a third of the anticipated Phase 3 work.

Also out of scope: GL posting of inventory adjustments (the COA accounts `1172`, `5107`, `9104` and the `default_inventory` slot are seeded and waiting, but nothing posts to them), multi-UOM conversion factors, and reorder-point automation (`inventory_stock.reorder_point` exists and is read by nothing).

# Fabrication & Installation Module — Design Spec

**Date:** 2026-07-22
**Branch:** `wednesday-work-planner`
**Status:** Design only — no Go or SQL written in this pass.
**Scope:** Sales → Order Received → Material Allocation → Stone Fabrication → Installation.

---

## 0. Context

StoneSuite's sales chain stops at the commercial boundary. `sales_order` tracks money
and fulfillment quantities (`DRFT→PAPV→APPV→OPEN→PART→FILL/CANC`,
`salesorder/transitions.go:10`), and `estimate → quote → salesorder → invoice` are all
built. What does **not** exist is the shop floor: once an order is paid, nothing in the
backend knows a slab was assigned to it, that it was templated, cut, polished, QC'd, or
installed.

Two partial stubs exist and are **not** sufficient:

- **`installation` v1 JSONB workflow** (`database/migrations/tenant/schema.sql:1660`) —
  5 generic states (`inst_scheduled / in_progress / on_hold / completed / cancelled`),
  no line items, no slab FKs, no typed steps.
- **`salesorder.Reserve`** (`salesorder/allocation.go:29`) — reserves a *decimal
  quantity* against `inventory_stock`, correctly row-locked, but explicitly "not yet
  wired to an HTTP action". Quantity-based reservation cannot express *which physical
  slab* is on a job, so vein matching, seam layout, and remnant recovery are all
  impossible today.

### 0.1 Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Fabrication lives in a **new relational module** (`fabrication/`), record type `FJOB` — not in `sales_order` | SO's state machine is commercial and feeds Invoice conversion; one order with 4 countertops needs 4 concurrent production states, which a single header status cannot hold |
| D2 | Add a **serialized `inventory_slab`** table | "Lock specific slabs" needs identity per physical slab; `inventory_stock` stays the aggregate rollup |
| D3 | The 16 checklist items are **rows in `fabrication_job_step`**, seeded on job creation | Per-step operator/timestamp/notes capture without inflating the 16-status machine |
| D4 | Reuse `authz.ResourceInstallation` | Already defined at `authz/catalog.go:39` **with all five grants seeded** (`authz/catalog.go:142-146`) |
| D5 | Every slab carries the **supplier's own ID** (`slab_supplier_code` + `slab_vendor_id`) alongside our internal `slab_serial` | Stone arrives from the quarry/supplier already tagged. Supplier codes collide across vendors, so they cannot be our primary key — but they are what a defect recall is traced by |
| D6 | A cut slab is **never resurrected**. Cutting mints a **child slab** with `slab_form='cut'`, `slab_status='available'` | Answers "release it so it can be reused, but inventory must say this stone is cut" without destroying the record of what was originally received. One slab can yield several usable pieces — a same-row status flip cannot represent that at all |

### 0.2 Reuse — do not rebuild

| Existing thing | Location | Used for |
|---|---|---|
| `ResourceInstallation` + 5 seeded grants | `authz/catalog.go:39`, `:142-146` | All RBAC for this module. **No catalog change needed**, which keeps `controllers/rbac_catalog_drift_test.go` green |
| `vendor` table | `schema.sql:3425` | `slab_vendor_id` FK — the supplier a slab was received from |
| `recordInScope` | `controllers/scope.go:29` | Row-level IDOR guard |
| `authSOByUUID` pattern | `controllers/salesorder.go:72` | Copy verbatim for `authFJByUUID` |
| `soFail` error mapper | `controllers/salesorder.go:104` | Copy for `fjFail` |
| Cross-resource double check | `controllers/salesorder.go:297` | Slab routes needing `inventory_item` too |
| `activeApproverCount` | `salesorder/approval.go` | The two fabrication approval gates |
| `Reserve` lock-then-check shape | `salesorder/allocation.go:29` | Reference for slab locking |
| `FormatNumber` | `salesorder/numbering.go` | `FJOB-000001` |
| `query.Request` / `FieldResolver` | `query/` | All list/search routes |

---

## 1. Status Model (16 codes)

New `lkp_record_type` row `('FJOB','fabricationjob','Fabrication Job')`, and 16
`lkp_record_status` rows against it.

| Business status | Code | Notes |
|---|---|---|
| DRAFT | `DRFT` | job created, not released |
| ORDER_RECEIVED | `ORCV` | initial state when spawned from an `APPV`/`OPEN` SO |
| MATERIAL_ALLOCATED | `MALC` | slabs **reserved**, not yet deducted |
| TEMPLATING_IN_PROGRESS | `TMPL` | |
| TEMPLATE_APPROVED | `TAPV` | **approval gate #1** |
| FABRICATION_READY | `FRDY` | nesting / vein layout signed off |
| CUTTING_IN_PROGRESS | `CUTG` | slabs **consumed**, stock deducted here |
| EDGING_AND_POLISHING | `EDGP` | |
| QUALITY_CONTROL_PENDING | `QCPD` | |
| QUALITY_CONTROL_PASSED | `QCPS` | **approval gate #2** |
| READY_FOR_SHIPPING | `RSHP` | |
| IN_TRANSIT | `TRAN` | |
| INSTALLATION_IN_PROGRESS | `INST` | |
| COMPLETED | `COMP` | terminal |
| ON_HOLD / BLOCKED | `HOLD` | resumable — see §1.2 |
| CANCELLED | `CANC` | terminal |

### 1.1 Happy path

Strictly linear:

```
DRFT → ORCV → MALC → TMPL → TAPV → FRDY → CUTG → EDGP → QCPD → QCPS → RSHP → TRAN → INST → COMP
```

`QCPD` additionally may go **backwards to `EDGP`** (rework on a failed QC). This is the
only backward edge in the machine and must be explicit in `allowedTransitions`.

### 1.2 `HOLD` is resumable, and the resume target is **not** caller-supplied

`HOLD` is reachable from every non-terminal state. Resuming returns the job to the state
it was in when held. That target cannot come from the static transition map, so it is
stored: `fabrication_job.job_held_from_status_id`, written on entry to `HOLD` and cleared
on resume.

> **Security note.** Letting the caller name the resume target is a
> privilege-escalation bug — a caller could resume from `HOLD` straight into `RSHP`
> and skip the `QCPS` sign-off. **`POST /resume` therefore takes no body and no target
> status at all.**

### 1.3 Terminal states

`CANC` is reachable from every non-terminal state. `COMP` and `CANC` are terminal with
empty transition sets, mirroring `salesorder/transitions.go:16`.

Mirror the existing `CanTransition` / `ValidateTransition` / `ErrInvalidTransition`
shape exactly.

---

## 2. Database Migration Changes

Appended to `database/migrations/tenant/schema.sql` as **migration `000035_fabrication_module`**
— the current highest block is `000034_sales_document_conversions` (schema.sql:4086).

Every statement `CREATE TABLE IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` /
`ON CONFLICT DO NOTHING`. **No `ALTER TABLE` that drops or retypes, and no
down-migration** — per CLAUDE.md, recovery is Neon PITR.

### 2.0 Lookup seeds

New `lkp_record_type` row `FJOB`, then the 16 statuses.

> The 16 status rows **must** be inserted with
> `INSERT ... SELECT ... FROM lkp_record_type WHERE record_type_code = 'FJOB'` —
> **not** a hardcoded `record_status_record_type = 18`. The existing seed block at
> `schema.sql:729` hardcodes ids 1–17; do not extend that pattern, it breaks on any
> tenant whose lookup rows were seeded out of order. Precedent for the correct
> subselect form is `schema.sql:2183`.

### 2.1 `inventory_slab` — serialized physical slab

New sibling of `inventory_stock`. Hybrid `SERIAL` + `UUID` PK, employee audit columns,
soft delete + CHECK constraint — copy the column conventions from `inventory_item`
(`schema.sql:2376`).

| Column group | Columns |
|---|---|
| Identity (ours) | `inventory_slab_id` SERIAL PK, `inventory_slab_uuid` UUID, `slab_serial` VARCHAR(50) — our printed/barcoded tag |
| Identity (supplier) | `slab_vendor_id` FK → `vendor` NULL, `slab_supplier_code` VARCHAR(80) — **the ID the supplier attached to the stone**, as printed |
| Receipt | `slab_received_at` DATE NULL, `slab_received_by` FK employee NULL, `slab_supplier_packing_ref` VARCHAR(80) |
| Material | `inventory_item_id` FK (the material/colour this slab is an instance of), `warehouse_id` FK |
| Vein-match grouping | `slab_bundle_id`, `slab_block_id`, `slab_lot` |
| Dimensions | `slab_length_mm`, `slab_width_mm`, `slab_thickness_mm` DECIMAL(10,2), all `CHECK (> 0)` |
| **Form** | `slab_form` VARCHAR(10) `CHECK IN ('full','cut')` — `full` = as received, uncut; `cut` = derived from another slab |
| Lineage | `slab_parent_slab_id` self-FK NULL |
| State | `slab_status` VARCHAR(20) `CHECK IN ('available','reserved','consumed','scrapped')` |
| Attributes | `slab_grade`, `slab_finish`, `slab_photo_key` (R2 object key), `slab_custom_fields` JSONB |
| Standard | audit columns, soft delete + CHECK, `slab_record_version` |

**`slab_form` is orthogonal to `slab_status`.** A usable offcut sitting on the rack is
`slab_form='cut'` + `slab_status='available'` — inventory says both "this is reusable"
and "this stone has been cut", which is exactly the requirement.

Constraints:
- `CHECK ((slab_form = 'cut') = (slab_parent_slab_id IS NOT NULL))` — the two facts
  cannot disagree
- `CHECK (slab_parent_slab_id IS DISTINCT FROM inventory_slab_id)` — no self-parent
- `CHECK (slab_supplier_code = '' OR slab_vendor_id IS NOT NULL)` — a supplier code is
  meaningless without knowing which supplier issued it

Indexes:
- **Partial unique on `LOWER(slab_serial)` WHERE `deleted_at IS NULL`** — exactly like
  `uq_inventory_item_sku_active` (`schema.sql:2403`), so a serial is reusable after soft
  delete. Applies to **every** slab including offcuts; each offcut gets its own printed tag.
- **Partial unique on `(slab_vendor_id, LOWER(slab_supplier_code))` WHERE
  `deleted_at IS NULL AND slab_form = 'full' AND slab_supplier_code <> ''`.**
  Three exclusions, each load-bearing:
  - `slab_form = 'full'` — offcuts **inherit** the parent's supplier code for recall
    traceability, so one full slab cut into three pieces yields three rows sharing a
    code. Without this exclusion the second cut fails.
  - `slab_supplier_code <> ''` — legacy stock received before this module existed has
    no supplier code; many blank rows must coexist.
  - Scoped by `slab_vendor_id` — two vendors can both ship a slab tagged `A-1024`.
- `(slab_vendor_id, slab_supplier_code)` **non-unique** — the recall lookup path (§4.10)
- `(inventory_item_id, slab_status)` partial on live rows
- `(slab_parent_slab_id)` — lineage walk
- `(slab_bundle_id)` for vein-match lookup
- GIN on `slab_custom_fields`

**Offcut serial generation:** `{parent_serial}-R{n}`, where `n` is the next free suffix
for that parent. Computed inside the transaction **while the parent row is locked
`FOR UPDATE`** (§4.2), so two concurrent recoveries from one slab cannot collide.

### 2.2 `fabrication_job` — header

| Column group | Columns |
|---|---|
| Identity | `fabrication_job_id` SERIAL, `fabrication_job_uuid` UUID, `fabrication_job_number` VARCHAR(20) (`FJOB-000001`) |
| Classification | `record_type` FK → `FJOB`, `fabrication_job_status` FK → `lkp_record_status` |
| Origin | `sales_order_id` FK → `sales_order` **NOT NULL**, `fabrication_job_customer_id` FK → `customer` |
| Hold | `job_held_from_status_id` FK → `lkp_record_status` NULL (§1.2) |
| Approval | `job_approval_status` VARCHAR(10) `'none'\|'pending'\|'approved'`, `job_approved_by` FK employee — mirrors `sales_order:2449` |
| Site snapshot | `job_site_addr_*` block, frozen at create like the SO billing snapshot |
| Scheduling | `job_template_date`, `job_fabrication_start`, `job_promised_install_date`, `job_actual_install_date` |
| Assignment | `job_owner_id`, `job_templater_id`, `job_fabricator_id`, `job_install_crew_id` (all FK `employee`) |
| Custom | `job_custom_fields` JSONB (≤15 keys, validated against `workflow_field_definitions`) |
| Standard | audit + soft delete + `record_version` |

Indexes — all partial `WHERE deleted_at IS NULL`, matching `schema.sql:2612-2619`:
`(sales_order_id)`, `(fabrication_job_status)`, `(job_owner_id)`, GIN on custom fields,
and the keyset tiebreaker `(created_at, id)`.

### 2.3 `fabrication_job_item` — one row per fabricated piece

Countertop, island, backsplash. FK `fabrication_job_id` + **nullable** FK
`sales_order_item_id` (a piece may have no separately billed line).

Carries `piece_number`, `piece_name`, `piece_type`, finished dimensions,
`edge_profile_id`, `sink_cutout_count`, `cooktop_cutout_count`, `seam_count`,
`piece_status`.

> Partial unique index on `(fabrication_job_id, piece_number) WHERE item_deleted_at IS NULL`
> — same reasoning as `uq_soi_line_active` (`schema.sql:2626`): update soft-deletes and
> re-inserts pieces reusing numbers, which a table-wide UNIQUE would reject.

### 2.4 `fabrication_job_slab` — the reservation join

FK `fabrication_job_id`, `fabrication_job_item_id`, `inventory_slab_id`.
`allocation_status` `CHECK IN ('reserved','consumed','released')`, `reserved_at/by`,
`consumed_at/by`, `yield_sq_ft`.

**Disposition block** — what happened to the stone when a job was cancelled after
cutting (§4.4):

| Column | Notes |
|---|---|
| `disposition` VARCHAR(20) NULL | `CHECK IN ('recovered','scrapped','delivered')` |
| `disposition_recorded_at` / `_by` | NULL until declared |
| `recovered_slab_id` FK → `inventory_slab` NULL | the offcut row minted by a `recovered` disposition |
| `recovered_sq_ft` DECIMAL NULL | must be > 0 exactly when `disposition = 'recovered'` |

`CHECK ((disposition = 'recovered') = (recovered_slab_id IS NOT NULL))`.

> **Partial unique index on `(inventory_slab_id) WHERE allocation_status IN ('reserved','consumed')`.**
> This index **is** the double-selling guard at the database layer. The
> application-level `FOR UPDATE` check in §4 is the friendly-error path; this is the
> backstop that holds under concurrency.

### 2.5 `fabrication_job_step` — the 16 checklist rows

Seeded per job on creation.

| Column | Notes |
|---|---|
| `fabrication_job_id` | FK |
| `fabrication_job_item_id` | FK **nullable** — per-piece steps (cutting, edging) vs per-job steps (dispatch) |
| `step_code` VARCHAR(24), `step_sequence` SMALLINT 1–16 | |
| `step_status` | `CHECK IN ('pending','in_progress','blocked','skipped','completed')` |
| `started_at/by`, `completed_at/by`, `step_notes` | operator + timing capture |
| `step_payload` JSONB | **one column instead of 16 bespoke tables** — see §5 for the per-step contract |

Unique `(fabrication_job_id, fabrication_job_item_id, step_code)`.

### 2.6 `fabrication_job_history`

From/to status trail, `action`, `actor_employee_id`, `snapshot` JSONB. Verbatim clone of
`sales_order_history` (`schema.sql:2600`).

### 2.7 `fabrication_job_approver` / `fabrication_job_approval`

Clones of the SO pair (`schema.sql:2662`, `:2678`), keyed to `FJOB` statuses.
Configured approvers on `TAPV` and `QCPS` are the two sign-off gates. Zero rows
configured = no gate — same semantics as SO.

### 2.8 Disposition of the v1 `installation` workflow

The seeded `installation` JSONB workflow (`schema.sql:1660`) is **left exactly as is,
`enabled = TRUE`**.

> This matches the actual precedent: when the relational Sales Order module superseded
> the v1 `sales_order` JSONB workflow, that workflow was left enabled and simply unused
> (`schema.sql:1623` seeds it `TRUE`; the comment at `schema.sql:2430` records the
> decision as "left in place, unused"). There is **no** precedent in this codebase for
> disabling a superseded v1 workflow, and existing tenant records may reference it.
>
> **Open item for the implementer:** two enabled fabrication surfaces will both appear
> in the workflow picker, which is a UI-confusion risk the v1 sales_order case also
> carries. Flag it to the frontend team rather than silently flipping `enabled`.

---

## 3. API Routes

All under `/api/tenant/fabrication-jobs`, registered in the tenant block of
`main.go:522` with `tenantChain(...)`, all guarded by `authz.ResourceInstallation`.

| Method | Path | Grant required |
|---|---|---|
| GET | `/api/tenant/fabrication-jobs` | `installation:read` |
| POST | `/api/tenant/fabrication-jobs/search` | `installation:read` |
| POST | `/api/tenant/fabrication-jobs` | `installation:create` |
| POST | `/api/tenant/sales-orders/{uuid}/fabricate` | `installation:create` + `sales_order:read` |
| GET | `/api/tenant/fabrication-jobs/{uuid}` | `installation:read` |
| PATCH | `/api/tenant/fabrication-jobs/{uuid}` | `installation:update` |
| DELETE | `/api/tenant/fabrication-jobs/{uuid}` | `installation:delete` |
| **PUT** | `/api/tenant/fabrication-jobs/{uuid}/fabrication/status` | `installation:transition` |
| POST | `/api/tenant/fabrication-jobs/{uuid}/hold` | `installation:transition` |
| POST | `/api/tenant/fabrication-jobs/{uuid}/resume` | `installation:transition` |
| POST | `/api/tenant/fabrication-jobs/{uuid}/approve` | `installation:transition` |
| GET | `/api/tenant/fabrication-jobs/{uuid}/steps` | `installation:read` |
| PATCH | `/api/tenant/fabrication-jobs/{uuid}/steps/{stepCode}` | `installation:update` |
| GET | `/api/tenant/fabrication-jobs/{uuid}/slabs` | `installation:read` + `inventory_item:read` |
| POST | `/api/tenant/fabrication-jobs/{uuid}/slabs` | `installation:update` + `inventory_item:update` |
| DELETE | `/api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}` | `installation:update` + `inventory_item:update` |
| POST | `/api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}/disposition` | `installation:update` + `inventory_item:update` |
| GET | `/api/tenant/fabrication-jobs/{uuid}/audit` | `installation:read` |
| GET/POST/PATCH/DELETE | `/api/tenant/inventory/slabs[/{uuid}]` | `inventory_item:*` |
| POST | `/api/tenant/inventory/slabs/{uuid}/scrap` | `inventory_item:update` |
| GET | `/api/tenant/inventory/slabs/{uuid}/lineage` | `inventory_item:read` |
| POST | `/api/tenant/inventory/slabs/recall-search` | `inventory_item:read` |

### 3.1 Two rules a copy-paste clone silently drops

1. **Slab routes require a second grant.** Reading or reserving slabs touches the
   inventory domain, so `installation:*` alone is insufficient — check `inventory_item`
   as well. A caller with `installation:read` but no `inventory_item:read` must not see
   warehouse stock they'd be denied at `GET /inventory/items`. Copy source:
   `controllers/salesorder.go:297`.
2. **Scope denial returns 404, never 403** — call
   `logSecurityEvent(r, "idor_denied", ...)` first, then `fail(w, 404, ...)`, so job ids
   cannot be enumerated.

### 3.2 Request / error contract

`PUT .../fabrication/status` body: `{"toStatusCode":"CUTG"}`.

| Condition | Status |
|---|---|
| Invalid transition, approval required | **409** |
| `*query.InvalidFilterError`, unknown status code, unavailable slab | **400** |
| Not a configured approver | **403** |
| Out of RBAC scope | **404** |

Clone `soFail` (`controllers/salesorder.go:104`) as `fjFail`. All responses carry
`success: false, message: "..."` on error.

---

## 4. Inventory Deduction Logic

The most defect-prone part of this module. Ordering is specified explicitly.

### 4.1 Reserve ≠ deduct

Two distinct events, two distinct statuses:

- **`MALC` (reserve)** — insert `fabrication_job_slab` rows with
  `allocation_status='reserved'`, flip `inventory_slab.slab_status` to `'reserved'`.
  `inventory_stock.quantity_on_hand` is **unchanged**. The slab is spoken for, not gone.
- **`CUTG` (consume)** — flip to `'consumed'` and **only now** decrement
  `inventory_stock.quantity_on_hand` for the `(inventory_item_id, warehouse_id)` pair.
  Physical stock leaves the building when the saw runs, not when the job is booked.

### 4.2 Locking order — fixed, to avoid deadlock

Inside the transition transaction, always in this order:

1. `fabrication_job` — `FOR UPDATE`
2. `inventory_slab` rows — `FOR UPDATE`, **ordered by `inventory_slab_id`**
3. `inventory_stock` — `FOR UPDATE`

Taking slab locks in id order is what stops two concurrent jobs reserving the same
bundle from deadlocking. `salesorder.Reserve` (`salesorder/allocation.go:29`) is the
reference for the lock-then-check shape.

### 4.3 Availability check

A slab is reservable only if `slab_status='available' AND deleted_at IS NULL` **and** no
live `fabrication_job_slab` row exists for it. Failure → `ClientError` → **400**, message
naming the slab serial.

> A unique-violation raised by the §2.4 partial index under a lost race must be mapped
> to the same **400**, **not** a 500.

### 4.4 Release and disposition — the full matrix

Two physically different situations, and conflating them is the bug this section exists
to prevent.

**Uncut material can be released. Cut material can only be *disposed of*.**

| Job status when cancelled | Slab state | Action |
|---|---|---|
| `DRFT`, `ORCV` | none allocated | nothing to do |
| `MALC` → `FRDY` | `reserved` | **Release:** `allocation_status='released'`, slab back to `'available'`. No stock change (nothing was ever deducted). Clone `salesorder/store_transition.go:80` |
| `CUTG` → `RSHP` | `consumed` | **Disposition required** — see below |
| `TRAN` | `consumed` | Disposition required (truck may return the material, or it may be written off) |
| `INST`, `COMP` | `consumed` | Disposition required (typically `delivered` — stone is glued to the customer's cabinets) |

> **The parent slab is never resurrected.** Once `slab_status='consumed'`, it stays
> consumed forever. That row records a full slab that physically no longer exists.
> Flipping it back to `'available'` would both lie about inventory and silently
> overwrite the dimensions the supplier actually shipped.

#### 4.4.1 Disposition is declared, never inferred

The backend **cannot** know whether cancelled stone is on the shop rack, on a truck, or
already installed at a customer's house. So cancelling a job past `CUTG` requires an
explicit per-slab disposition, supplied by the operator:

| `disposition` | Meaning | Effect |
|---|---|---|
| `recovered` | Usable material came back to the yard | **Mint a child slab** — `slab_form='cut'`, `slab_status='available'`, `slab_parent_slab_id` = the consumed slab, inheriting material, vendor, and **supplier code**. Increment `inventory_stock` by `recovered_sq_ft` |
| `scrapped` | Broken or unusable | No child row. Parent stays `consumed`. **No stock change** — it was already deducted at `CUTG` |
| `delivered` | Already at the customer site | No child row, no stock change |

**The cancel transition is rejected with 409 while any consumed slab on the job still
has `disposition IS NULL`.** This is deliberate friction: it stops cut stone from
silently vanishing from inventory.

#### 4.4.2 Idempotency

Record disposition with `UPDATE ... WHERE fabrication_job_slab_id = $1 AND disposition IS NULL`.
**Zero rows affected → 409**, never a second offcut. This is the guard against a
double-submitted cancel minting duplicate inventory.

#### 4.4.3 `HOLD` does **not** release — a reversal from §1.2's first draft

An earlier draft of this spec released reserved slabs on `HOLD`. **That was wrong** and
is corrected here:

> If a hold released the reservation, another job could take the slab, and the held job
> would become **unresumable** — its vein-matched material is gone. A hold is normally
> short (awaiting a customer decision or a part), and shop practice is that the slab
> stays physically set aside for that job.

So: **reservations survive `HOLD` in every state.** Only `CANC` releases.

*Mitigation for the obvious downside* (material stranded under a forgotten hold): a
`GET /api/tenant/fabrication-jobs?status=HOLD&heldLongerThan=<days>` report so stranded
reservations are visible. Auto-expiry of holds is **out of scope** — silently releasing
someone's vein-matched slab on a timer is worse than the stranding it fixes.

### 4.5 Offcut recovery — one mechanism, two triggers

Minting a child slab is the *same code path* whether it fires from normal completion or
from cancellation. Do not write it twice.

| Trigger | When |
|---|---|
| **Normal yield** | At consume (`CUTG`), if `yield_sq_ft` < the slab's area, the leftover becomes an offcut |
| **Cancellation recovery** | At `CANC` with `disposition='recovered'` (§4.4.1) |

Both produce: child `inventory_slab`, `slab_form='cut'`, `slab_status='available'`,
`slab_parent_slab_id` set, vendor + supplier code + material inherited, serial
`{parent}-R{n}`, and an `inventory_stock` increment — all in the **same transaction** as
the triggering event.

**Guards:**
- Recovered area must be `> 0` and `<=` the parent's area → else **400**
- Child dimensions must each be `<=` the parent's → else **400**
- An offcut may itself be cut later (`slab_parent_slab_id` chains arbitrarily deep);
  this is normal and needs no special handling beyond the self-reference CHECK

### 4.6 Stock floor

`inventory_stock.quantity_on_hand` carries `CHECK (quantity_on_hand >= 0)`
(`schema.sql:2419`). Any deduction that would breach it is a bug in the reservation math
— catch it as a `ClientError` **before** the write. Never let the CHECK violation surface
as a 500.

### 4.7 Sales-order fulfillment coupling

Reaching `COMP` bumps `sales_order_item.line_fulfilled_quantity` for the linked SO lines,
which lets the order reach `PART`/`FILL` naturally.

> **Guard:** `chk_soi_fulfilled` caps `line_fulfilled_quantity <= quantity`
> (`schema.sql:2575`). Clamp before writing.

### 4.8 Over-allocation release at `COMP`

A job may reserve 3 slabs and only cut 2. On reaching `COMP`, **any slab still
`reserved` (never consumed) is released back to `available`** — same path as §4.4's
uncut release.

Without this, every completed job quietly strands its spare material and available
stock drifts down forever. This is the single most likely long-run inventory leak in
the module.

### 4.9 Breakage scenarios

| Scenario | Handling |
|---|---|
| Slab breaks in the yard, never allocated | `POST /inventory/slabs/{uuid}/scrap` → `slab_status='scrapped'`, decrement `inventory_stock`. Independent of any job |
| Slab breaks **during** cutting | It is already `consumed` (stock deducted at `CUTG`). Record `disposition='scrapped'` on that `fabrication_job_slab`, then **allocate a replacement slab to the still-live job** |
| Replacement allocation | `POST /fabrication-jobs/{uuid}/slabs` must therefore be legal at `CUTG` and later — **not only at `MALC`**. A clone that gates slab-add on `MALC` breaks every real breakage recovery |
| Offcut breaks | Same as any slab: scrap it. Parent lineage is untouched |

### 4.10 Recall traceability

Supplier codes inherited by offcuts (§2.1) plus the `slab_parent_slab_id` chain answer
the question a supplier defect notice actually poses:

> "Vendor V's lot `A-1024` has fissures — where did all of it end up?"

`POST /inventory/slabs/recall-search` takes `{vendorId, supplierCode}` and returns, via
a **recursive CTE** over `slab_parent_slab_id`: every descendant slab, its current
status, and every fabrication job and sales order that touched any of them.

This is why supplier code is inherited rather than blanked on offcuts, and why the
`(slab_vendor_id, slab_supplier_code)` non-unique index exists.

### 4.11 Unit-of-measure precondition ⚠️

The deduction arithmetic in §4.1 and §4.5 assumes `inventory_item.inventory_item_unit_id`
for any slab-tracked material resolves to an **area unit** (sq ft, m²) — because a full
slab is deducted and a fractional offcut is added back.

> If a tenant configures a stone item in a **count** unit, offcut recovery produces a
> fractional count — `2.4 slabs on hand` — which is meaningless and corrupts every
> downstream availability check.

**This is not hypothetical.** `lkp_unit` (`schema.sql:2315-2319`) seeds
`('Slab','SLAB','count')` right alongside `('Square Foot','SQFT','area')`. "Slab" is the
obvious-looking choice for a stone item and it is the **wrong** one — it is
`unit_category = 'count'`.

**Required:** the slab-catalog create/update handler must assert the parent
`inventory_item`'s unit has `lkp_unit.unit_category = 'area'`, and reject with **400**
naming the offending unit otherwise. No new lookup row is needed — `SQFT` and `SQM`
already exist and `unit_category` already distinguishes them.

---

## 5. The 16 Fabrication Sub-Steps

**Invariant:**

> Advancing to status *S* requires every step whose `step_sequence` precedes *S*'s gate
> to be `completed` or `skipped`. **Steps are the precondition; status is the
> consequence.** A status transition never silently completes a step, and completing a
> step never auto-advances status.

`skipped` requires a non-empty `step_notes` — an auditable reason. Otherwise the
checklist can be bypassed silently, which defeats the point of tracking it.

| # | Step | `step_code` | Gates status | Grain | `step_payload` contract |
|---|---|---|---|---|---|
| 1 | Order Intake & Verification | `INTAKE_VERIFY` | `ORCV` | job | verified dimensions vs customer spec, discrepancy list |
| 2 | Inventory Check & Slab Allocation | `SLAB_ALLOCATE` | `MALC` | job | — (state lives in `fabrication_job_slab`) |
| 3 | Digital/Physical Templating | `TEMPLATING` | `TMPL` | piece | site measurements, seam placements, template file key |
| 4 | Slab Layout & Programming | `SLAB_LAYOUT` | `TAPV` → `FRDY` | piece | nesting plan, vein-match approval, approver, layout image key |
| 5 | Primary Saw Cutting | `SAW_CUTTING` | `CUTG` | piece | bridge-saw program id, operator, start/end |
| 6 | CNC Route / Waterjet | `CNC_CUTTING` | `CUTG` | piece | sink cutouts, cooktop cutouts, radius cuts, machine id |
| 7 | Edge Profiling & Polishing | `EDGE_PROFILE` | `EDGP` | piece | edge profile executed (eased / bullnose / mitered), linear ft |
| 8 | Manual Detailing & Hand Polishing | `HAND_POLISH` | `EDGP` | piece | artisan, areas detailed |
| 9 | Rodding & Reinforcement | `RODDING` | `EDGP` | piece | rod locations, rod material, epoxy batch |
| 10 | Layout Match & Dry Run | `DRY_RUN` | `QCPD` | job | seam alignment result, photo keys |
| 11 | Final Quality Control | `FINAL_QC` | `QCPS` | piece | dimension check, defect list (scratches / fissures / blemishes), pass/fail |
| 12 | Sealing & Treatment | `SEALING` | `RSHP` | piece | sealer product, batch number, coats, cure time |
| 13 | Bundling & A-Frame Loading | `BUNDLE_LOAD` | `RSHP` | job | A-frame / bundle id, securing checklist |
| 14 | Dispatch & Logistics | `DISPATCH` | `TRAN` | job | carrier, driver, tracking ref, departure time |
| 15 | Site Installation | `SITE_INSTALL` | `INST` | job | placement log, leveling readings, seam-glue batch, crew |
| 16 | Post-Install Sign-off | `SIGN_OFF` | `COMP` | job | customer acceptance, signature R2 key, punch list |

`step_payload` is validated server-side by `step_code` — an unrecognized key in a payload
is a **400**, not a silent accept.

---

## 6. Backend Test Cases

Table-driven with `testify`, per CLAUDE.md. DB-backed cases sit behind the `dbtest` build
tag with `TEST_DATABASE_URL`, skipping cleanly when unset.

### 6.1 Pure / no DB — `fabrication/transitions_test.go`, `numbering_test.go`

- [ ] Full 16×16 transition matrix: every legal edge allowed, every illegal edge rejected
- [ ] `COMP` and `CANC` are terminal — no outbound edge
- [ ] `HOLD` reachable from all 14 non-terminal states; `CANC` likewise
- [ ] `QCPD→EDGP` rework edge allowed
- [ ] `QCPD→RSHP` (skipping the `QCPS` gate) rejected
- [ ] `FormatNumber` zero-padding → `FJOB-000001`

### 6.2 Deduction — `dbtest`

- [ ] Reserve at `MALC` leaves `quantity_on_hand` **unchanged**; slab becomes `reserved`
- [ ] Consume at `CUTG` decrements exactly once; **re-running the transition does not double-deduct**
- [ ] **Double-sell:** two concurrent transactions reserving the same slab → one succeeds, one gets 400; exactly one live `fabrication_job_slab` row remains
- [ ] Cancel **before** `CUTG` releases slabs to `available`, no stock change
- [ ] Cancel **after** `CUTG` is **rejected 409** while any consumed slab has `disposition IS NULL` (§4.4.1)
- [ ] **`HOLD` never releases a reservation**, in every state (§4.4.3) — and resume re-acquires nothing because nothing was let go
- [ ] Offcut on partial yield at `CUTG` restores the correct `quantity_on_hand`
- [ ] Deduction that would drive stock negative → 400, not 500, and rolls back cleanly
- [ ] **`COMP` releases still-`reserved` (never cut) slabs** — the over-allocation leak (§4.8)

**Disposition (§4.4.1)**
- [ ] `recovered` mints exactly one child, `slab_form='cut'`, `slab_status='available'`, and increments stock by `recovered_sq_ft`
- [ ] Child **inherits vendor + supplier code + material** from the parent
- [ ] Child serial is `{parent}-R1`; a second recovery from the same parent yields `-R2`, not a collision
- [ ] **Parent stays `consumed`** under every disposition — never resurrected
- [ ] `scrapped` and `delivered` mint no child and change no stock
- [ ] **Double-submit:** recording disposition twice → 409, exactly one offcut exists
- [ ] `recovered_sq_ft` of 0, negative, or > parent area → 400
- [ ] Child dimensions exceeding the parent's → 400
- [ ] An offcut can itself be allocated, cut, and recovered (chain depth ≥ 2)

**Supplier identity (§2.1)**
- [ ] Two vendors may both ship supplier code `A-1024` — both insert fine
- [ ] Same vendor + same code on two **full** slabs → rejected
- [ ] Same vendor + same code across a parent and its offcuts → **allowed** (the `slab_form='full'` exclusion)
- [ ] Many blank supplier codes coexist (legacy stock)
- [ ] `slab_supplier_code` set with NULL `slab_vendor_id` → rejected by CHECK
- [ ] `slab_form='cut'` with NULL parent, and `'full'` with a parent → both rejected
- [ ] Self-parent → rejected

**Breakage & units (§4.9, §4.11)**
- [ ] Scrapping an unallocated slab decrements stock; scrapping a `consumed` one does not double-decrement
- [ ] **A replacement slab can be allocated at `CUTG`**, not only at `MALC`
- [ ] Creating a slab whose item unit is `SLAB` (category `count`) → **400**; `SQFT` → accepted
- [ ] `recall-search` returns the full descendant tree plus touching jobs/orders

### 6.3 Status / approval — `dbtest`

- [ ] Transition writes exactly one `fabrication_job_history` row with correct from/to
- [ ] `TAPV` with configured approvers blocks until approved → 409 `ErrApprovalRequired`
- [ ] `QCPS` gate likewise; non-approver → 403 + `approval_denied` security log
- [ ] Zero configured approvers = no gate
- [ ] Hold → resume returns to `job_held_from_status_id`
- [ ] **Resume endpoint rejects any caller-supplied target status** (§1.2)
- [ ] Advancing past a gate with an incomplete prior step → 409
- [ ] `skipped` without `step_notes` → 400
- [ ] Reaching `COMP` bumps SO fulfilled quantities and clamps at line quantity

### 6.4 Security — `controllers/fabrication_test.go` (the clone-drift cases)

- [ ] Every route 401s without JWT, 403s without the `installation` grant
- [ ] `own`-scoped caller gets **404 (not 403)** on another owner's job, on **every** single-record route — including `/steps`, `/slabs`, `/hold`, `/resume`, `/approve`. This is exactly what a copy-paste clone drops.
- [ ] Slab routes 403 for a caller holding `installation:*` but **not** `inventory_item:read`
- [ ] `logSecurityEvent` fires on permission denial, IDOR denial, and approval denial
- [ ] **Filter ⨯ scope is ANDed:** an `own`-scoped caller filtering `ownerId = <someone else>` gets zero rows, never someone else's
- [ ] Unknown filter key → 400 `*query.InvalidFilterError`, never 500
- [ ] Cursor pagination caps at `query.MaxLimit` (100)

### 6.5 Schema — `migration-auditor` agent + fresh-DB apply

- [ ] `schema.sql` applies twice in a row cleanly (idempotent)
- [ ] **No hardcoded `record_type_id`** in the new seeds (§2.0)
- [ ] All new tables have soft-delete CHECK constraints and keyset indexes
- [ ] pgvector present + `--single-transaction` + fresh DB per package, or verification produces convincing false failures

---

## 7. Implementation Task Checklist

Sequenced by FK dependency. Nothing here is done yet.

### 7.1 Schema — `database/migrations/tenant/schema.sql`, block `000035_fabrication_module`

- [ ] 1. `lkp_record_type` row `FJOB` (`ON CONFLICT DO NOTHING`)
- [ ] 2. 16 `lkp_record_status` rows via subselect on `record_type_code='FJOB'`
- [ ] 3. `inventory_slab` — supplier identity block, `slab_form`, 3 CHECKs, partial unique on `LOWER(slab_serial)`, partial unique on `(vendor, supplier_code)` with the 3 exclusions, lineage + recall indexes
- [ ] 4. `fabrication_job` + 5 indexes
- [ ] 5. `fabrication_job_item` + partial unique on `(job_id, piece_number)`
- [ ] 6. `fabrication_job_slab` + **partial unique double-sell guard** + disposition block (§2.4)
- [ ] 7. `fabrication_job_step` + unique `(job, item, step_code)`
- [ ] 8. `fabrication_job_history`
- [ ] 9. `fabrication_job_approver` / `fabrication_job_approval`
- [ ] 10. Run the `migration-auditor` agent

### 7.2 Go package — `fabrication/` (each file < 300 lines, per CLAUDE.md)

- [ ] 11. `types.go` — `Job`, `JobItem`, `Slab`, `Step`, input structs
- [ ] 12. `transitions.go` — the 16-state map, `CanTransition`, `ValidateTransition`
- [ ] 13. `numbering.go` — `FormatNumber` → `FJOB-000001`
- [ ] 14. `resolver.go` — `query.FieldResolver` whitelist (filterable + sortable fields)
- [ ] 15. `store_create.go` — create + spawn-from-SO + seed the 16 steps
- [ ] 16. `store_update.go` — header + pieces (soft-delete/re-insert pattern)
- [ ] 17. `store_transition.go` — transition, hold, resume, gate checks
- [ ] 18. `allocation.go` — reserve / consume / release / over-allocation release at `COMP` (§4.8)
- [ ] 18b. `recovery.go` — the **single** offcut-minting path shared by normal yield and cancellation recovery (§4.5), serial `-R{n}` generation, disposition recording with the `WHERE disposition IS NULL` idempotency guard
- [ ] 18c. `lineage.go` — recursive-CTE recall search (§4.10)
- [ ] 19. `steps.go` — step update + per-`step_code` payload validation
- [ ] 20. `store_search.go` + `store_sqlbuild.go` — keyset search through `query/`
- [ ] 21. `approval.go` — clone of `salesorder/approval.go`

### 7.3 Controllers — `controllers/`

- [ ] 22. `fabrication.go` — `authFJ`, `authFJByUUID` (IDOR guard, 404 on scope denial), `fjFail`, CRUD
- [ ] 23. `fabrication_transition.go` — status / hold / resume / approve
- [ ] 24. `fabrication_slabs.go` — slab routes **with the `inventory_item` double check**, allocate/deallocate/disposition
- [ ] 25. `fabrication_steps.go` — step read/update
- [ ] 26. `fabrication_audit.go` — audit trail
- [ ] 27. `inventory_slabs.go` — standalone slab catalog CRUD + **area-unit validation (§4.11)** + scrap + lineage + recall-search

### 7.4 Wiring & verification

- [ ] 28. Register all routes in `main.go` tenant block (next to `sales-orders`, ~line 522)
- [ ] 29. Confirm **no** `authz/catalog.go` change is needed (`ResourceInstallation` already seeded)
- [ ] 30. Write the §6 tests
- [ ] 31. Run the `module-drift-checker` agent against `fabrication`
- [ ] 32. Run the `tenancy-security-reviewer` agent
- [ ] 33. Run the `filter-invariant-checker` agent (new `FieldResolver`)
- [ ] 34. `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run`

---

## 8. Out of Scope

- **Auto-expiry of holds** (§4.4.3) — a stale-hold *report* is in scope; silently
  releasing vein-matched material on a timer is not
- **Retroactive correction of a recorded disposition.** Disposition is write-once
  (§4.4.2). Fixing a mistake needs a stock-adjustment module, which does not exist yet
- Nesting geometry solving — only the *approval* of a layout is tracked, not its computation
- Item Receipt (`IRCT`) module. The record type is seeded in `lkp_record_type` but
  **no `item_receipt` table exists yet**, so §2.1 captures receipt facts directly on the
  slab (`slab_received_at`, `slab_supplier_packing_ref`) rather than creating a dangling
  FK. When that module lands, add `slab_received_on_receipt_id` additively
- Scheduling optimization / crew capacity planning
- Customer-facing job status portal
- Retiring the v1 `installation` JSONB workflow (§2.8)

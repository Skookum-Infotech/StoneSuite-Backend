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
| D7 | `inventory_stock` for slab-tracked items is a **projection of an append-only `inventory_slab_ledger`**, never written ad-hoc | Without this the slab table and the stock table are two ledgers with no reconciliation — see §2.9. Makes double-counting structurally impossible rather than test-detectable |
| D5 | Every slab carries the **supplier's own ID** (`slab_supplier_code` + `slab_vendor_id`) alongside our internal `slab_serial` | Stone arrives from the quarry/supplier already tagged. Supplier codes collide across vendors, so they cannot be our primary key — but they are what a defect recall is traced by |
| D6 | A cut slab is **never resurrected**. Cutting mints a **child slab** with `slab_form='cut'`, `slab_status='available'` | Answers "release it so it can be reused, but inventory must say this stone is cut" without destroying the record of what was originally received. One slab can yield several usable pieces — a same-row status flip cannot represent that at all |

### 0.2 Reuse — do not rebuild

| Existing thing | Location | Used for |
|---|---|---|
| `ResourceInstallation` + 5 seeded grants | `authz/catalog.go:39`, `:142-146` | All RBAC for this module. **No catalog change needed** |
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
only backward edge **in `allowedTransitions`** and must be explicit there.

> Resume (§1.2) also moves a job backwards, but it is **not** an entry in
> `allowedTransitions` — see §1.2 for why it is validated separately.

### 1.2 `HOLD` is resumable, and the resume target is **not** caller-supplied

`HOLD` is reachable from every non-terminal state **except `HOLD` itself** — that is
**13** source states, not 14.

> **`HOLD → HOLD` must be rejected.** Re-holding a held job would overwrite
> `job_held_from_status_id` with `HOLD`'s own id, making resume a self-loop and the job
> **permanently unresumable**. This is a data-destroying self-edge, not a harmless no-op.

Resuming returns the job to the state it was in when held, read from
`fabrication_job.job_held_from_status_id` — written on entry to `HOLD`, cleared on resume.

> **Security note.** Letting the caller name the resume target is a
> privilege-escalation bug — a caller could resume from `HOLD` straight into `RSHP`
> and skip the `QCPS` sign-off. **`POST /resume` takes no body and no target status.**

**Resume is not an entry in `allowedTransitions`.** `allowedTransitions["HOLD"]` contains
only `{CANC}`. Resume is a separate operation validated by its own rule:

> The resume target is whatever `job_held_from_status_id` holds. It is never
> caller-supplied, never validated against the transition map, and is guaranteed legal
> because the job was in that state moments earlier.

This keeps the static map minimal (§1.1's "only backward edge" claim stays true of the
map) and makes §6.1's 16×16 matrix writable: the matrix tests `allowedTransitions` only,
and resume gets its own tests in §6.3.

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

> The 16 status rows **must** resolve the type id with
> `(SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'FJOB')` —
> **not** a hardcoded `record_status_record_type = 18`. The existing seed block at
> `schema.sql:729` hardcodes ids 1–17; do not extend that pattern, it breaks on any
> tenant whose lookup rows were seeded out of order.
>
> **There is no existing `INSERT ... SELECT` seed precedent for `lkp_record_status`
> in this file** — `schema.sql:2183` is an `UPDATE ... WHERE ... IN (SELECT ...)`, not a
> seed, and must not be copied as one. The new block has to combine the subselect with
> the multi-row `VALUES` + `ON CONFLICT (record_status_code, record_status_record_type)
> DO NOTHING` shape used at 729. Write it as:
>
> ```sql
> INSERT INTO lkp_record_status (record_status_code, record_status_name,
>     record_status_record_type, record_status_is_active, record_status_is_system,
>     record_status_created_by)
> SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
> FROM (VALUES ('DRFT','Draft'), ('ORCV','Order Received') /* … 16 rows … */)
>      AS v(code, name)
> CROSS JOIN lkp_record_type rt
> WHERE rt.record_type_code = 'FJOB'
> ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;
> ```

### 2.1 `inventory_slab` — serialized physical slab

New sibling of `inventory_stock`. Hybrid `SERIAL` + `UUID` PK, employee audit columns,
soft delete + CHECK constraint — copy the column conventions from `inventory_item`
(`schema.sql:2376`).

| Column group | Columns |
|---|---|
| Identity (ours) | `inventory_slab_id` SERIAL PK, `inventory_slab_uuid` UUID, `slab_serial` VARCHAR(50) — our printed/barcoded tag |
| Identity (supplier) | `slab_vendor_id` FK → `vendor` NULL, `slab_supplier_code` VARCHAR(80) **NOT NULL DEFAULT ''** — the ID the supplier attached to the stone, as printed |
| Area | `slab_area` DECIMAL(14,3), `slab_area_unit_id` FK → `lkp_unit` — computed from the mm dimensions at create time, in the parent item's unit (§4.11.1) |
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
  meaningless without knowing which supplier issued it. **This is why the column is
  `NOT NULL DEFAULT ''`:** were NULLs allowed, `NULL = ''` evaluates to NULL, the whole
  expression is NULL, and PostgreSQL *passes* a CHECK that is not false — so a NULL code
  with a NULL vendor would slip through. The same NULL would also silently fall out of
  the `slab_supplier_code <> ''` index predicate below
- `slab_parent_slab_id` is **immutable after insert** (enforced in the handler, §4.10)

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
`consumed_at/by`, `yield_area` (in the item's unit — §4.11.1, never assumed ft²).

**Disposition block** — what happened to the stone when a job was cancelled after
cutting (§4.4):

| Column | Notes |
|---|---|
| `disposition` VARCHAR(20) NULL | `CHECK IN ('recovered','scrapped','delivered')` |
| `disposition_recorded_at` / `_by` | NULL until declared |
| `recovered_slab_id` FK → `inventory_slab` NULL | the offcut row minted by a `recovered` disposition |
| `recovered_area` DECIMAL(14,3) NULL | in the **item's own unit** (§4.11.1), never assumed ft² |

Both halves of the invariant need a constraint — prose is not enforcement:

```sql
CHECK ((disposition = 'recovered') = (recovered_slab_id IS NOT NULL))
CHECK ((disposition = 'recovered') = (recovered_area IS NOT NULL AND recovered_area > 0))
```

> Without the second CHECK a `recovered` disposition with a NULL or zero area passes the
> database, and the only guard is application code — contrary to this section's own
> doctrine that the DB is the backstop that holds under concurrency.

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

**Uniqueness needs two partial indexes, not one constraint.**

> `UNIQUE (fabrication_job_id, fabrication_job_item_id, step_code)` **enforces nothing
> for job-grain steps.** PostgreSQL treats NULLs as distinct in a unique constraint, and
> per §5 seven of the sixteen steps are job-grain with a NULL `fabrication_job_item_id`
> — steps 1, 2, 10, 13, 14, 15, 16. Those are exactly the steps gating `ORCV`, `MALC`,
> `QCPD`, `RSHP`, `TRAN`, `INST` and `COMP`, so unlimited duplicates would be allowed
> on the highest-consequence rows.

```sql
CREATE UNIQUE INDEX ... ON fabrication_job_step (fabrication_job_id, fabrication_job_item_id, step_code)
    WHERE fabrication_job_item_id IS NOT NULL;
CREATE UNIQUE INDEX ... ON fabrication_job_step (fabrication_job_id, step_code)
    WHERE fabrication_job_item_id IS NULL;
```

Two partial indexes rather than `NULLS NOT DISTINCT`, which needs PG15+; this form works
on any supported version.

### 2.9 `inventory_slab_ledger` — the only writer of slab-tracked stock

**Without this table the design has two unreconciled ledgers.** `inventory_slab` rows
record physical stones; `inventory_stock.quantity_on_hand` records a number. Nothing
connects them, so stock gets spent down at `CUTG` and never stocked up on receipt —
it drifts to the `CHECK (quantity_on_hand >= 0)` floor (`schema.sql:2419`) while slabs
sit physically on the rack, and every consume then fails with a 400.

Append-only, one row per stock-affecting slab event:

| Column | Notes |
|---|---|
| `inventory_slab_ledger_id` SERIAL PK | |
| `inventory_slab_id` FK, `inventory_item_id` FK, `warehouse_id` FK | |
| `event` VARCHAR(20) | `CHECK IN ('received','consumed','recovered','scrapped','adjusted')` |
| `quantity_delta` DECIMAL(14,3) | **signed**, in the item's own unit (§4.11) |
| `fabrication_job_slab_id` FK NULL | the allocation that caused it, when there is one |
| `occurred_at`, `actor_employee_id` | |

**Invariant:** for any slab-tracked item,
`inventory_stock.quantity_on_hand = SUM(quantity_delta)` over its ledger rows. Every
write to `quantity_on_hand` is accompanied by exactly one ledger row **in the same
transaction**, and no code path writes it any other way.

**Idempotency by construction** — partial unique indexes make the double-count bugs
unrepresentable rather than merely tested-against:

- `UNIQUE (inventory_slab_id) WHERE event = 'received'` — a slab is received once
- `UNIQUE (inventory_slab_id) WHERE event = 'consumed'` — consumed once, so a re-run
  transition cannot deduct twice
- `UNIQUE (inventory_slab_id) WHERE event = 'scrapped'` — scrapped once
- `UNIQUE (fabrication_job_slab_id) WHERE event = 'recovered'` — one recovery per allocation

Deltas per event:

| Event | Delta | When |
|---|---|---|
| `received` | **+ slab area** | slab row created via catalog CRUD (§4.1a) |
| `consumed` | **− slab area** | `CUTG` (§4.1) |
| `recovered` | **+ recovered area** | offcut minted (§4.5) — note the offcut *also* gets its own `received` row, so recovery writes one or the other, never both |
| `scrapped` | **− slab area** | only if the slab was still counted (§4.9) |

> **`recovered` vs `received` — pick one.** An offcut is a new `inventory_slab` row, so
> its creation would naturally emit `received`. That is the row to write. The
> `recovered` event exists only for the `fabrication_job_slab` audit link and carries
> `quantity_delta = 0` when a `received` row was written for the same stone. **Never
> emit both with non-zero deltas** — that is C4-1's phantom stock.

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
> (`schema.sql:1623` seeds it `TRUE`; the comment at `schema.sql:2432` records the
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

### 4.1a Receipt — where stock comes from

**Creating an `inventory_slab` row increments `inventory_stock.quantity_on_hand` by the
slab's area, and writes a `received` ledger row (§2.9), in the same transaction.**

This is the only way slab-tracked stock ever goes up from outside a job. Omitting it is
what makes the rest of the arithmetic a one-way ratchet to zero.

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
2. `fabrication_job_slab` rows — `FOR UPDATE`, ordered by `inventory_slab_id`
3. `inventory_slab` rows — `FOR UPDATE`, **ordered by `inventory_slab_id`**
4. `inventory_stock` — `FOR UPDATE`
5. `inventory_slab_ledger` — insert only, no lock needed (append-only, §2.9)

> `fabrication_job_slab` belongs in this order and was missing from an earlier draft.
> §4.3's availability rule reads it, and §2.4's partial unique index is enforced on it,
> so a transaction that touches slabs without holding its rows can still interleave.

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
| `MALC` → `FRDY` | `reserved` | **Release:** `allocation_status='released'`, slab back to `'available'`. No stock change and **no ledger row** (nothing was ever deducted). Clone `salesorder/store_transition.go:80` |
| `CUTG` → `RSHP` | `consumed` | **Disposition required** — see below |
| `TRAN` | `consumed` | Disposition required (truck may return the material, or it may be written off) |
| `INST` | `consumed` | Disposition required (typically `delivered` — stone is glued to the customer's cabinets) |
| **`HOLD`** | **depends** | **Dispatch on `job_held_from_status_id`, not on `HOLD`.** Reservations survive a hold (§4.4.3), so a job held at `TMPL` still has `reserved` slabs and takes the release path, while one held at `EDGP` has `consumed` slabs and requires disposition. Reading the literal current status here would take the wrong branch every time |

> **`COMP` is absent from this table on purpose.** §1.3 makes `COMP` terminal with an
> empty transition set, so a completed job can never be cancelled and the row would be
> unreachable. Material questions after completion are stock adjustments, not
> cancellations — and those are out of scope (§8).

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
| `recovered` | Usable material came back to the yard | **Mint a child slab** — `slab_form='cut'`, `slab_status='available'`, `slab_parent_slab_id` = the consumed slab, inheriting material, vendor, and **supplier code**. Increment `inventory_stock` by `recovered_area` and write the §2.9 ledger row |
| `scrapped` | Broken or unusable | No child row. Parent stays `consumed`. **No stock change** — it was already deducted at `CUTG` |
| `delivered` | Already at the customer site | No child row, no stock change |

**The cancel transition is rejected with 409 while any consumed slab on the job still
has `disposition IS NULL`.** This is deliberate friction: it stops cut stone from
silently vanishing from inventory.

#### 4.4.2 Idempotency

Record disposition with `UPDATE ... WHERE fabrication_job_slab_id = $1 AND disposition IS NULL`.
**Zero rows affected → 409**, never a second offcut. This is the guard against a
double-submitted cancel minting duplicate inventory.

#### 4.4.3 Disposition is only legal while cancelling

The endpoint must be **gated on the job actually being on its way to `CANC`**, not
callable at any time.

> The trap: §4.4.1 rejects the cancel *until* dispositions exist, so the endpoint has to
> be callable before the job is `CANC`. Left unguarded, an operator can record
> `recovered` on a perfectly healthy job at `CUTG`, mint an offcut, inflate stock — and
> then carry the job on to `COMP`, double-counting the stone. §4.4.2 makes it
> unrepeatable and §8 puts correction out of scope, so **the error is permanent**.

Required guard: the job must be in a `cancelling` intent state — set
`fabrication_job.job_cancel_requested_at` when the cancel is first attempted and
rejected for missing dispositions, and accept dispositions **only** while that is set.
Clearing it (abandoning the cancel) is allowed **only** when no disposition has yet been
recorded; once one has, the job is committed to cancellation.

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
| **Normal yield** | At consume (`CUTG`), if `yield_area` < the slab's area, the leftover becomes an offcut |
| **Cancellation recovery** | At `CANC` with `disposition='recovered'` (§4.4.1) |

Both produce: child `inventory_slab`, `slab_form='cut'`, `slab_status='available'`,
`slab_parent_slab_id` set, vendor + supplier code + material inherited, serial
`{parent}-R{n}`, and an `inventory_stock` increment — all in the **same transaction** as
the triggering event.

**Guards:**

- **Recovered area is checked against the parent's *remaining* area, not its original
  area.** This is the single most dangerous arithmetic error in the module:

  > 50 ft² slab consumed at `CUTG` (stock −50). Normal yield 20 ft², so trigger 1 mints
  > a 30 ft² offcut (stock +30). The job is then cancelled at `EDGP`; the parent is
  > still `consumed`, so §4.4 demands a disposition. An operator declares `recovered`
  > with 50 ft² — which passes a naive "≤ parent area" check — and stock goes +50.
  > **Net result: +30 ft² of stone that does not exist.**

  The guard is therefore:
  `recovered_area <= parent_area - COALESCE(SUM(area of already-minted children), 0)`,
  computed with the parent row locked `FOR UPDATE`. A parent whose remaining area is
  already 0 can only be dispositioned `scrapped` or `delivered`.

- Recovered area must be `> 0` → else **400**
- Child dimensions must each be `<=` the parent's → else **400**
- An offcut may itself be cut later; `slab_parent_slab_id` chains arbitrarily deep.
  **The parent pointer is immutable after insert** — see §4.10 for why a mutable one
  is a denial-of-service on the recall search.

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

**Clamping silently is not acceptable when several jobs share one SO line.** D1's whole
rationale is that one order with four countertops becomes multiple `fabrication_job`
rows, and §2.3 links pieces back to `sales_order_item_id`. If each job bumps the same
line at `COMP`, a silent clamp discards the excess with no error and no record — and the
sales order can read `FILL` purely from over-reporting.

> Required: if the computed bump would exceed the line quantity, clamp **and** write a
> `fabrication_job_history` row with `action = 'fulfillment_clamped'` recording the
> requested and applied amounts. An operator must be able to find out why the numbers
> disagree.

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
| Slab breaks in the yard, never allocated | `POST /inventory/slabs/{uuid}/scrap` → `slab_status='scrapped'`, `scrapped` ledger row, decrement stock |
| Slab breaks while **reserved** to a live job | **Scrap must not blindly decrement.** The slab is still counted in stock, so scrapping decrements once — but it must **also release the reservation** (`allocation_status='released'`). Otherwise the row stays `reserved`, the job later reaches `CUTG`, and §4.1 deducts the *same physical slab a second time*, walking stock toward the §4.6 floor |
| Slab breaks **during** cutting | Already `consumed`, stock already deducted at `CUTG`. Scrap here is a **no-op on stock** — the `UNIQUE (inventory_slab_id) WHERE event='consumed'` ledger index and the absence of a second delta are what prevent the double-decrement. Record `disposition='scrapped'`, then **allocate a replacement slab to the still-live job** |
| Replacement allocation | `POST /fabrication-jobs/{uuid}/slabs` must therefore be legal at `CUTG` and later — **not only at `MALC`**. A clone that gates slab-add on `MALC` breaks every real breakage recovery |
| Offcut breaks | Same as any slab: scrap it. Parent lineage is untouched |

### 4.10 Recall traceability

Supplier codes inherited by offcuts (§2.1) plus the `slab_parent_slab_id` chain answer
the question a supplier defect notice actually poses:

> "Vendor V's lot `A-1024` has fissures — where did all of it end up?"

`POST /inventory/slabs/recall-search` takes `{vendorId, supplierCode}` and returns, via
a **recursive CTE** over `slab_parent_slab_id`: every descendant slab, its current
status, and every fabrication job and sales order that touched any of them.

> **Cycle protection is mandatory.** `CHECK (slab_parent_slab_id IS DISTINCT FROM
> inventory_slab_id)` blocks only 1-cycles. §3 exposes a generic
> `PATCH /inventory/slabs/{uuid}`, so a 2-cycle (A→B, B→A) is otherwise reachable and
> the recursive CTE **will not terminate** — a trivial denial of service on a
> tenant-facing endpoint.
>
> Two required defences, both:
> 1. **`slab_parent_slab_id` is immutable after insert.** The update handler must reject
>    any attempt to change it (400), and it is omitted from the PATCH payload struct.
> 2. The CTE carries a **`CYCLE slab_id SET is_cycle USING path`** clause (PG14+) *and*
>    a depth cap, so a cycle introduced by a future migration or direct SQL degrades to
>    a truncated result instead of a hung connection.

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

#### 4.11.1 Area-vs-area is a second, subtler trap

Passing the count-vs-area check is **not** sufficient. Both `SQFT` and `SQM` are
`'area'`, and `inventory_stock.quantity_on_hand` is denominated in *the item's own unit*.

> If the transfer columns are named `yield_sq_ft` / `recovered_sq_ft` and their values
> are added to an `SQM`-configured item's `quantity_on_hand`, every recovery is wrong by
> a factor of **10.764**.

Two consequences for §2.1 and §2.4:

1. **Name the columns `yield_area` and `recovered_area`, not `*_sq_ft`.** They are
   denominated in the parent item's unit, and the name must not assert otherwise.
2. **`inventory_slab` stores dimensions in mm but needs an explicit area.** Add
   `slab_area` DECIMAL(14,3) plus `slab_area_unit_id` FK → `lkp_unit`, populated at
   create time by converting mm² into the parent item's unit. Deriving area on the fly
   from mm dimensions at each use site invites a different conversion in each one.

All ledger `quantity_delta` values (§2.9) are in the item's unit. **No code path may mix
units**; the conversion happens exactly once, at slab creation.

---

## 5. The 16 Fabrication Sub-Steps

**Invariant:**

> Advancing to status *S* requires every step whose `step_sequence` precedes *S*'s gate
> to be `completed` or `skipped`. **Steps are the precondition; status is the
> consequence.** A status transition never silently completes a step, and completing a
> step never auto-advances status.

`skipped` requires a non-empty `step_notes` — an auditable reason. Otherwise the
checklist can be bypassed silently, which defeats the point of tracking it.

**Rework must reopen steps, or the backward edge tracks nothing.** On `QCPD → EDGP`
(§1.1), steps 7, 8, 9 and 11 are already `completed`, so without a reset the job can
walk straight back to `QCPD` with no work recorded and the rework is invisible.

> A `QCPD → EDGP` transition **resets steps 7–9 and 11 to `pending`** for the affected
> pieces, in the same transaction, and writes a `fabrication_job_history` row with
> `action = 'rework'`. Previous step rows are not deleted — their operator and timing
> stay in history, which is the point of tracking rework at all.

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

- [ ] Full 16×16 matrix over **`allowedTransitions` only** (resume is not in the map, §1.2)
- [ ] `COMP` and `CANC` are terminal — no outbound edge
- [ ] `HOLD` reachable from **13** non-terminal states — **`HOLD→HOLD` rejected** (§1.2); `CANC` reachable from all 14
- [ ] `allowedTransitions["HOLD"] == {CANC}` exactly
- [ ] `QCPD→EDGP` rework edge allowed; `QCPD→RSHP` (skipping `QCPS`) rejected
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
- [ ] `recovered` mints exactly one child, `slab_form='cut'`, `slab_status='available'`, and increments stock by `recovered_area`
- [ ] Child **inherits vendor + supplier code + material** from the parent
- [ ] Child serial is `{parent}-R1`; a second recovery from the same parent yields `-R2`, not a collision
- [ ] **Parent stays `consumed`** under every disposition — never resurrected
- [ ] `scrapped` and `delivered` mint no child and change no stock
- [ ] **Double-submit:** recording disposition twice → 409, exactly one offcut exists
- [ ] `recovered_area` of 0, negative, or > parent's **remaining** area → 400
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

**Ledger & stock reconciliation (§2.9, §4.1a)** — the critical class
- [ ] **Creating a slab increments stock** and writes exactly one `received` row (§4.1a). Without this every other case passes while real stock ratchets to zero
- [ ] For any item, `quantity_on_hand == SUM(ledger.quantity_delta)` — assert after every scenario below
- [ ] **Double-recovery:** 50-unit slab, yield 20 → 30-unit offcut; cancel at `EDGP`; recovery of 50 → **400**, because remaining area is 0. Stock must not gain 30 phantom units (C4-1)
- [ ] Recovery capped at `parent_area − SUM(children)`, not `parent_area`
- [ ] A parent with 0 remaining area accepts only `scrapped` / `delivered`
- [ ] Re-running a `CUTG` transition writes no second `consumed` row (unique index)
- [ ] An offcut gets a `received` row **or** a non-zero `recovered` row, never both

**Breakage & units (§4.9, §4.11)**
- [ ] Scrapping an **unallocated** slab decrements stock once
- [ ] **Scrapping a `reserved` slab releases the allocation**, so the job reaching `CUTG` cannot deduct the same stone twice (C4-3)
- [ ] Scrapping an already-`consumed` slab is a **no-op on stock**
- [ ] **A replacement slab can be allocated at `CUTG`**, not only at `MALC`
- [ ] Creating a slab whose item unit is `SLAB` (category `count`) → **400**; `SQFT` → accepted
- [ ] **`SQM`-configured item:** recovery arithmetic stays in the item's unit — no ft²/m² mixing (§4.11.1)
- [ ] `recall-search` returns the full descendant tree plus touching jobs/orders
- [ ] **`PATCH` of `slab_parent_slab_id` → 400** (immutable), and a hand-inserted 2-cycle does not hang the CTE (§4.10)

**Constraint-level (§2.1, §2.4, §2.5)** — these must fail at the DB, not just in Go
- [ ] Two job-grain steps with the same `(job, step_code)` and NULL item → **rejected** by the partial index (§2.5). This is the one a single 3-column UNIQUE silently allows
- [ ] `recovered` disposition with NULL or 0 `recovered_area` → **rejected** by CHECK
- [ ] `slab_supplier_code` cannot be NULL (`NOT NULL DEFAULT ''`), so the vendor CHECK cannot be bypassed by a NULL

### 6.3 Status / approval — `dbtest`

- [ ] Transition writes exactly one `fabrication_job_history` row with correct from/to
- [ ] `TAPV` with configured approvers blocks until approved → 409 `ErrApprovalRequired`
- [ ] `QCPS` gate likewise; non-approver → 403 + `approval_denied` security log
- [ ] Zero configured approvers = no gate
- [ ] Hold → resume returns to `job_held_from_status_id`
- [ ] **Resume endpoint rejects any caller-supplied target status** (§1.2)
- [ ] **Cancel from `HOLD` dispatches on `job_held_from_status_id`**: held at `TMPL` → release path; held at `EDGP` → disposition required (§4.4)
- [ ] **Disposition rejected on a live job** with no cancel requested (§4.4.3), then accepted once cancel has been attempted
- [ ] `QCPD→EDGP` **resets steps 7–9 and 11 to `pending`** and logs `action='rework'` (§5)
- [ ] Two jobs on one SO line over-reporting at `COMP` → clamped **and** `fulfillment_clamped` logged (§4.7)
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
- [ ] 8. `fabrication_job_history` (action CHECK must allow `rework` and `fulfillment_clamped`)
- [ ] 8b. **`inventory_slab_ledger` + its 4 partial unique indexes** (§2.9) — the reconciliation backbone
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

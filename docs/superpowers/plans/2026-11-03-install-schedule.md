# Field and Installation Tooling (Roadmap Item 5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an office scheduler book install, template and service visits for fabrication jobs onto a crew calendar. The calendar shows week, day and phone views and warns about double-booking. Every change is audited and notifies the crew. This is slice 1. Slices 2 (mobile job pages with photo capture) and 3 (offline) are outlined at high level only.

**Architecture:** A new backend package `installschedule/` owns three new tenant tables: `install_appointment`, `install_appointment_crew` and `install_appointment_history`. It reads fabrication jobs and employees but never writes them. It serves them through a new RBAC resource `install_appointment` under `/api/tenant/install-appointments`.

Conflict checks run inside the write transaction and are serialized with advisory locks. Overlaps are a soft warning that the caller must acknowledge. Times are stored as UTC `TIMESTAMPTZ`, but the API speaks tenant-local wall clock.

The WebUI adds a page at Calendar › Install Schedule. It reuses the Activity Planner grid components through a new card kind, and adds a "Visits" tab on the fabrication job.

**Tech Stack:** Go, pgx/v5, PostgreSQL (Neon), testify. React 19, TanStack Query, Tailwind, vitest.

**Spec:** Roadmap item 5 ("Field and installation tooling", size L), as briefed on 2026-11-03. There is no separate spec doc, so the design decisions live in sections 1–12 of this file. Related plans this one must not collide with:
- `docs/superpowers/plans/fabrication-execution-ledger.md`
- `docs/superpowers/plans/2026-10-01-fabrication-workspace.md` (Tasks 10–13)

**Repos:**
- Backend: `/Users/prasannakumarnagaboyina/pkumardev/StoneSuite-Backend/.claude/worktrees/wonderful-cray-b0df95` (HEAD `5f49838`)
- WebUI: `/Users/prasannakumarnagaboyina/pkumardev/StoneSuite-WebUI/.claude/worktrees/feat-install-schedule` (HEAD `92baf36`)

All paths below are relative to the named repo root.

## Global Constraints

- Never write `fabrication_job` rows. Never bump `fabrication_job_record_version`. Never touch piece `production_stage`, `fabrication_action_event`, `fabrication_delivery_obligation` or handover tables.
- Schema changes are append-only, appended to the end of `database/migrations/tenant/schema.sql`. Use `CREATE TABLE IF NOT EXISTS` and `CREATE [UNIQUE] INDEX IF NOT EXISTS`. Destructive DDL and down-migrations are forbidden. The schema must apply twice in one transaction (CI does exactly this).
- No `CURRENT_DATE`, `now()::date` or `LOCALTIMESTAMP` in any new SQL. Tenant "today" and tenant zone are computed in Go from `companyprofile.Get(ctx,pool).Timezone`, with the `config.AppConfig.AutomationDefaultTimezone` fallback.
- Every handler sits behind `tenantChain` (main.go:512) and calls `authz.Require`-style checks before acting. Single-record denial returns **404** and logs `logSecurityEvent(r, "idor_denied", ...)`.
- Error JSON is `{"success": false, "message": "..."}`. Status codes are drawn from 200/201/400/401/403/404/409/500.
- New code logs with `log/slog` only. CI rejects newly added `log.Printf` in `controllers/`, `services/` and `middleware/`.
- Every `.go` and `.tsx`/`.ts` file is at most 300 lines. WebUI components take at most 5 props and use Tailwind only.
- `domainevent.Emit` gets the transaction (`pgx.Tx`), never the pool. `workflow.LogAuditFull` and notifications run after commit.
- Every `services.NotificationRequest` sets `ActorUserID` and an `IdempotencyKey`. Write responses that may email include `"emailSent": bool`.
- Slice 1 adds no new third-party dependency to either repo (no drag-and-drop library, no `btree_gist`).

## Review Focus

1. **DST edges.** In America/Chicago, a booking at 2026-03-08 02:30 (does not exist) must be rejected with 400 and a clear message. A booking at 2026-11-01 01:30 (happens twice) must resolve to the first occurrence (CDT, `-05:00`) and round-trip unchanged. Tested in Task B2.
2. **Two schedulers booking the same crew member at the same moment.** Exactly one gets a clean 201. The other gets the soft-conflict 409 naming the first booking, never two silent overlaps. Tested in Task B4 with concurrent goroutines.
3. **Own-scope installer reading a colleague's appointment by uuid.** The response is 404 (not 403), and an `idor_denied` security event is logged. The same applies to the history endpoint. Tested in Task B7.
4. **Stale edit.** Two tabs edit one appointment. The second PATCH with the old `expectedVersion` gets 409 `stale_version`, not a lost update. Tested in Tasks B5 and W4.
5. **A crew member with no linked login (`employee_user_id IS NULL`).** The booking succeeds, the notification is skipped with an slog line, and `emailSent` is false. It never errors. Tested in Task B6.

---

## 1. Context

What exists today (all verified in code):

- **Fabrication job** (`fabrication_job`, schema.sql:4269-4317) carries single-value planning fields:
  - `job_template_date` and `job_promised_install_date` (DATE)
  - `job_actual_install_date` (DATE)
  - `job_install_crew_id` (one `employee` FK)

  It has its own status machine in `fabrication/transitions.go`: DRFT, ORCV, MALC, TMPL, TAPV, FRDY, CUTG, EDGP, QCPD, QCPS, RSHP, TRAN, INST, COMP, HOLD, CANC. There is **no "scheduled" status**, and edits bump `fabrication_job_record_version`, which the v2 ledger commands use as `expectedVersion`.
- **The v1 `installation` JSONB workflow seed** (schema.sql:1662-1695, states `inst_scheduled` … `inst_cancelled`) is a dormant legacy stub. Fabrication jobs do not use it, and the WebUI only uses `workflowKey="installation"` for enablement. Slice 1 does not touch it.
- **Sales-order Activity Planner:**
  - Data: `sales_order_schedule` (schema.sql:9231-9263) holds 11 planned activities per order with wall-clock `TIMESTAMP`, plus `sales_order.sales_order_scheduled_install_date`.
  - Backend: `salesorder/schedule.go` and `salesorder/schedule_feed.go` (window ≤ 93 days, cap 5000 plus `truncated`).
  - Frontend: `src/pages/calendar/ActivityPlannerPage.tsx`, which already has an "Installation" tab.

  This is a **plan** per order. It is not a crew-bookable appointment and has no crew, so it cannot detect a double-booking.
- **RBAC:** `authz/catalog.go` has `ResourceInstallation = "installation"` (the fabrication job resource, lines 299-303), scopes `all|own`, and only `super_admin` is seeded (`authz/seed.go`).
- **Precedents this plan copies:**
  - Window feed: `salesorder/schedule_feed.go`.
  - Second-resource auth: `controllers/fabrication_source_so.go` (`authSourceSalesOrder`).
  - IDOR helper: `controllers/scope.go:29` `recordInScope`.
  - Tenant today: `controllers/customer_transactions.go:93` `tenantToday`.
  - Zone math: `automation/today.go` `TenantLocation`.
  - Employee notification lookup: `approvalchain/notify.go` `actorContact`.
  - DB IDOR test harness: `controllers/customer_transactions_dbtest_test.go`.

## 2. Goals, non-goals, anti-goals

**Goals (slice 1)**
- G1. A scheduler books a visit (template, install or service) for a fabrication job: date/time or all-day, duration, one lead plus optional crew, notes.
- G2. A week/day/month calendar of visits, filterable by crew member, plus a phone view.
- G3. The system warns about crew double-booking and about overlapping visits on the same job before saving. The scheduler can override with acknowledgement, and the override is recorded.
- G4. Reschedule, reassign, cancel (with reason) and mark complete. Every change has a history row, an audit log entry and a domain event.
- G5. Crew members with a login are notified (email/in-app) when they are booked, moved, removed or a visit is cancelled.
- G6. A fabrication job shows its visits (Visits tab) and can create one from there.

**Non-goals (slice 1)**
- Route optimization, travel time, crew capacity planning. Spec 2026-07-22 §8 explicitly puts these out of scope.
- Working hours, time off and availability calendars (open question Q3).
- Customer-facing notifications or portal display (designed in §9, built later).
- Syncing appointments into `sales_order_schedule` or the Activity Planner "Installation" tab (Q8).
- Recurring appointments.
- Drag-and-drop rescheduling (see §10.3).
- Photo capture and offline mode (slices 2 and 3).

**Anti-goals (must never happen)**
- A1. Booking, moving or completing a visit changes fabrication job status, `job_install_crew_id`, `job_promised_install_date`, `job_actual_install_date` or the job's record version.
- A2. Visit "completed" being read as installation sign-off. It only means the visit took place. Sign-off stays with fabrication v2 (`SIGN_OFF` step, `record_signoff` in workspace Task 10).
- A3. A hard DB constraint that makes a legitimate override impossible.
- A4. Any appointment data, crew names or notes reaching the customer portal in slice 1.

## 3. Collision avoidance with the fabrication v2 ledger

| Fabrication v2 asset (workspace plan) | Slice 1 rule |
|---|---|
| `fabrication_job_record_version`, `expectedVersion` commands | Read-only join. Appointments carry their own `record_version`. |
| Task 10: `fabrication/handover_*.go`, `fabrication/installation.go`, `controllers/fabrication_handovers.go`, `FabricationInstallationPanel.tsx`, actions `assign_installer`, `record_installation`, `record_signoff` | Do not create or edit these files or action names. The new package is `installschedule/`, not `fabrication/`. |
| Task 11: `fabrication/installer_access.go`, `middleware/installer.go`, `src/pages/installer/InstallerWorkPage.tsx`, `installerService.ts` | Do not create these. Slice 2 builds on Task 11 instead of duplicating it (§13). |
| Task 12: responsive workspace, `FabricationActionPanel`, edits to `FabricationJobDetailPage.tsx` | The only edit to `FabricationJobDetailPage.tsx` is adding one entry to `TABS` (line 61) plus one render branch. That edit is preceded by a split (Task W6), and the Task 12 owner is told about it. |
| `job_install_crew_id`, `job_promised_install_date` | Displayed as read-only context in the dialog ("Promised install: …", "Job crew lead: …"). Never written. |
| `fabrication_job_history` action CHECK | Untouched. Appointment history has its own table. |

Future link: when Task 10 `assign_installer` lands, it can read `install_appointment_crew` for the next `install` visit. Nothing in slice 1 depends on that.

## 4. Data model

### 4.1 Options weighed

**Option A: columns on `fabrication_job`** (`job_install_starts_at`, `job_install_ends_at`, `job_install_crew_ids INT[]`). Rejected for four reasons:
- Every edit bumps `fabrication_job_record_version` (via fabrication store update paths) and so invalidates in-flight v2 commands. This violates A1.
- It allows only one visit per job, but templates, installs and return or service visits are distinct.
- It cannot index per-employee overlap.
- It adds to a table owned by another plan.

**Option B: reuse `sales_order_schedule`** (activity `installation`). Rejected for four reasons:
- It is keyed by sales order, not job, so several jobs on one SO would collide.
- It has no crew.
- It is wall-clock `TIMESTAMP` with no zone.
- Its statuses are pending/in_progress/completed, with no cancel or audit reason.

**Option C (recommended): dedicated appointment tables.** This allows multiple visits per job and many crew per visit, gives appointments their own optimistic version, and keeps the history in its own table. It sits next to the job without writing to it.

### 4.2 Tables (Option C)

These are new tables, so CHECKs and FKs go **inside** the `CREATE TABLE IF NOT EXISTS` body; no guarded `ADD CONSTRAINT` is needed. They are appended at the end of `database/migrations/tenant/schema.sql` under a header comment `-- Install schedule (roadmap item 5, slice 1)`.

`install_appointment`
| column | type | notes |
|---|---|---|
| install_appointment_id | SERIAL PK | |
| install_appointment_uuid | UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE | `gen_random_uuid()` is already used throughout tenant schema.sql (verified). |
| fabrication_job_id | INT NOT NULL REFERENCES fabrication_job(fabrication_job_id) | |
| visit_type | VARCHAR(16) NOT NULL DEFAULT 'install' CHECK IN ('template','install','service') | |
| appointment_status | VARCHAR(16) NOT NULL DEFAULT 'scheduled' CHECK IN ('scheduled','completed','cancelled') | |
| starts_at, ends_at | TIMESTAMPTZ NOT NULL | Half-open `[starts_at, ends_at)`. CHECK `ends_at > starts_at`. CHECK `ends_at - starts_at <= interval '14 days'`. |
| all_day | BOOLEAN NOT NULL DEFAULT false | |
| local_start_date, local_end_date | DATE NOT NULL | Inclusive local dates, computed in Go. CHECK `local_end_date >= local_start_date`. |
| timezone | VARCHAR(64) NOT NULL | IANA name used at booking time. |
| notes | TEXT NULL | CHECK `char_length(notes) <= 2000`. |
| cancel_reason | TEXT NULL | CHECK `char_length(cancel_reason) <= 500`. |
| cancelled_at / cancelled_by | TIMESTAMPTZ / INT REFERENCES employee(employee_id) | |
| completed_at / completed_by | TIMESTAMPTZ / INT REFERENCES employee(employee_id) | |
| create_request_id | UUID NULL | Idempotency for create. |
| created_at / created_by, updated_at / updated_by | TIMESTAMPTZ NOT NULL DEFAULT now() / INT NOT NULL REFERENCES employee | |
| record_version | INT NOT NULL DEFAULT 1 | |

Named CHECK `chk_ia_status_stamps`:
- `(appointment_status='cancelled') = (cancelled_at IS NOT NULL)`
- `(appointment_status='completed') = (completed_at IS NOT NULL)`
- `cancel_reason IS NULL OR appointment_status='cancelled'`

Indexes:
- `uq_ia_create_request` UNIQUE (fabrication_job_id, create_request_id) WHERE create_request_id IS NOT NULL
- `idx_ia_job_start` (fabrication_job_id, starts_at)
- `idx_ia_live_start` (starts_at) WHERE appointment_status <> 'cancelled'

`install_appointment_crew`
| column | type | notes |
|---|---|---|
| install_appointment_id | INT NOT NULL REFERENCES install_appointment ON DELETE CASCADE | |
| employee_id | INT NOT NULL REFERENCES employee(employee_id) | |
| crew_role | VARCHAR(8) NOT NULL DEFAULT 'member' CHECK IN ('lead','member') | |
| assigned_at / assigned_by | TIMESTAMPTZ NOT NULL DEFAULT now() / INT NOT NULL REFERENCES employee | |

PK (install_appointment_id, employee_id). Indexes:
- `uq_iac_one_lead` UNIQUE (install_appointment_id) WHERE crew_role='lead'
- `idx_iac_employee` (employee_id, install_appointment_id)

`install_appointment_history`
| column | type |
|---|---|
| install_appointment_history_id | BIGSERIAL PK |
| install_appointment_id | INT NOT NULL REFERENCES install_appointment |
| action | VARCHAR(16) NOT NULL CHECK IN ('create','reschedule','reassign','update','cancel','complete') |
| from_starts_at, from_ends_at, to_starts_at, to_ends_at | TIMESTAMPTZ NULL |
| reason | TEXT NULL (≤500) |
| conflicts_acknowledged | JSONB NULL (the conflict list the actor overrode) |
| snapshot | JSONB NOT NULL (post-change appointment plus crew ids) |
| actor_employee_id | INT NOT NULL REFERENCES employee |
| notify_notification_ids | TEXT[] NULL (same shape as `document_sends`, schema.sql:8402) |
| at | TIMESTAMPTZ NOT NULL DEFAULT now() |

Index `idx_iah_appt_at` (install_appointment_id, at DESC).

One PATCH may yield several history rows (for example reschedule plus reassign), all with the same `at`.

The JSONB columns here are internal system snapshots, not custom fields. The "never hand-craft JSONB" rule targets custom-field data. They are still marshalled from typed Go structs, never string-built.

### 4.3 RBAC data
- No role backfill (dark launch, §12). The optional backfill SQL, if the PO picks it in Q5, follows the `bin_transfer` precedent at schema.sql:9682-9695: `INSERT … SELECT … WHERE NOT EXISTS … ON CONFLICT DO NOTHING`, granting `install_appointment:read` scope `all` to roles that already hold `installation:read`.

## 5. Backend API

### 5.1 Routes (all wired in `main.go` next to the fabrication block at 1399-1431, each wrapped in `tenantChain`)

| Method + path | Handler | Permission |
|---|---|---|
| GET `/api/tenant/install-appointments?from=YYYY-MM-DD&to=YYYY-MM-DD[&employeeId=N][&visitType=…][&includeCancelled=true]` | `Feed` | install_appointment:read (scope-filtered) |
| GET `/api/tenant/install-appointments/crew-options` | `CrewOptions` | install_appointment:create OR :update |
| POST `/api/tenant/install-appointments` | `Create` | install_appointment:create, plus installation:read with scope on the job |
| GET `/api/tenant/install-appointments/{uuid}` | `Get` | install_appointment:read plus recordInScope |
| PATCH `/api/tenant/install-appointments/{uuid}` | `Update` | install_appointment:update plus scope |
| POST `/api/tenant/install-appointments/{uuid}/cancel` | `Cancel` | install_appointment:transition plus scope |
| POST `/api/tenant/install-appointments/{uuid}/complete` | `Complete` | install_appointment:transition plus scope |
| GET `/api/tenant/install-appointments/{uuid}/history` | `History` | install_appointment:read plus scope |
| GET `/api/tenant/fabrication-jobs/{uuid}/install-appointments` | `ListForJob` | installation:read via the existing `authFJByUUID`, plus install_appointment:read |

Register `crew-options` before `{uuid}`. Assumption: the router is Go 1.22 `http.ServeMux` patterns as used in main.go, where a literal segment beats a wildcard. Verify against neighbouring routes.

Path order and method patterns follow the fabrication block. Every new route is added to `route_reachability_test.go` and `body_cap_routes_test.go` if those guards enumerate routes. Assumption: they enumerate. Read both before Task B7.

### 5.2 Request and response shapes

Create body:
```json
{ "fabricationJobId": "<job uuid>", "visitType": "install",
  "startLocal": "2026-11-10T08:00", "endLocal": "2026-11-10T12:00",
  "allDay": false, "crew": [{"employeeId": 12, "role": "lead"}, {"employeeId": 15, "role": "member"}],
  "notes": "…", "requestId": "<uuid>", "acknowledgeConflicts": false }
```
- With `allDay: true`, `startLocal`/`endLocal` are `YYYY-MM-DD` and the end date is inclusive.
- PATCH body: any of `startLocal`, `endLocal`, `allDay`, `crew` (full replacement list), `notes`, `visitType`, plus the required `expectedVersion` and optional `acknowledgeConflicts`, `reason`.
- Cancel body: `{expectedVersion, reason}`, where reason is required, 1–500 chars.
- Complete body: `{expectedVersion}`.

Appointment JSON (`installschedule.Appointment`):
```json
{ "id": "<uuid>", "fabricationJobId": "<job uuid>", "jobNumber": "…", "customerName": "…",
  "siteCity": "…", "visitType": "install", "status": "scheduled",
  "startsAt": "2026-11-10T14:00:00Z", "endsAt": "2026-11-10T18:00:00Z",
  "startLocal": "2026-11-10T08:00", "endLocal": "2026-11-10T12:00", "timezone": "America/Chicago",
  "allDay": false, "localStartDate": "2026-11-10", "localEndDate": "2026-11-10",
  "crew": [{"employeeId": 12, "name": "…", "role": "lead"}], "notes": "…",
  "cancelReason": null, "version": 3 }
```
- Write responses: `{success: true, appointment, emailSent}`.
- Feed: `{success: true, appointments: [...], truncated: bool, timezone}`.

Assumption: the `jobNumber` column is not yet verified. Task B3 must read `fabrication/types.go` and `fabrication/store.go` for the job's display number field and map it. If no number column exists, fall back to the job uuid prefix the WebUI already shows.

Site and customer fields come from `job_site_customer_name`, `job_site_addr_city` and similar columns, which are verified in schema.sql:4269-4317.

### 5.3 Validation (pure, in `installschedule/validate.go`) → 400
- `visitType` must be in the enum. `crew` has at most 10 entries, unique employee ids, at most one `lead`, and every id an active, non-system, non-deleted employee (checked in the store → 400 `invalid_crew`).
- Crew may be empty (Q1 default). The lead is optional.
- `end > start`, the duration is at most 14 days, and minutes are multiples of 15 (Q7). Notes are at most 2000 chars.
- Start must be no earlier than tenant today minus 1 day. Backdating more than a day is rejected, but completing late is allowed.
- The fabrication job must be live (`fabrication_job_deleted_at IS NULL`) and not terminal (`IsTerminal` COMP/CANC) for create and reschedule. Otherwise 409 `job_closed`.
- Feed `from <= to`, and the window is at most 93 days (matches `salesorder/schedule_feed.go`).
- Status rules:
  - Only `scheduled` appointments can be updated, cancelled or completed. Anything else gets 409 `invalid_status`.
  - `complete` is allowed only when `starts_at <= now`. Otherwise 409 `not_started`.

### 5.4 Error envelope
`{success:false, message, code}`, where `code` is one of:
- `invalid_input` (400), `invalid_time` (400, DST gap)
- `not_found` (404)
- `stale_version` (409, also returns `currentVersion`)
- `conflict` (409, soft; see §6)
- `job_closed` (409), `invalid_status` (409), `not_started` (409)
- `forbidden` (403, permission missing)

`code` is additive to the required `success` and `message` fields.

## 6. Conflict detection

**Overlap rule.** Two intervals overlap iff `a.starts_at < b.ends_at AND b.starts_at < a.ends_at`. Intervals are half-open, so back-to-back 8–12 and 12–4 do not conflict. Cancelled appointments never conflict, and neither do completed ones in the past.

- **Crew conflict:** for each crew employee, any other live appointment containing that employee that overlaps.
- **Job conflict:** any other live appointment on the same fabrication job that overlaps. This catches a template and an install booked at the same time.

**Mechanism: transactional check, not an exclusion constraint.**
- An exclusion constraint on (employee_id, tstzrange) needs `btree_gist`, and the schema has only the `vector` extension (schema.sql:2160). The `excl_ap_no_overlap` precedent at 6797-6804 avoids btree_gist by having no equality column.
- Crew rows live in a child table without the time range, so a constraint would need denormalized columns.
- A constraint is a hard block, and the product default is a soft warning (Q2). Hard blocking an override is anti-goal A3.

**Concurrency.** Inside the write transaction, before the conflict query:
1. `SELECT pg_advisory_xact_lock(hashtext($1))` with key `'ia:job:<id>'`
2. For each employee id in ascending order (old ∪ new crew on update): the same call with key `'ia:emp:<id>'`

The fixed lock order prevents deadlocks.

Then run the conflict query, then the writes. Two concurrent bookings of one employee therefore serialize, and the second one sees the first. This is the verified precedent from `salesorder/store_create_dupguard.go:46` and `vendorbill/store_create_dupguard.go:49`.

**Response shapes.**
- **Soft conflict** (default): 409
  ```json
  { "success": false, "code": "conflict", "requiresAcknowledgement": true,
    "message": "This visit overlaps 2 other bookings.",
    "conflicts": [{ "type": "crew", "employeeId": 12, "employeeName": "…",
                    "appointmentId": "<uuid>", "jobNumber": "…",
                    "startLocal": "…", "endLocal": "…" },
                  { "type": "job", "appointmentId": "<uuid>", "jobNumber": "…", "startLocal": "…", "endLocal": "…" }] }
  ```
  The client resubmits with `acknowledgeConflicts: true`. The server **re-runs** the check under the locks and saves. The conflicts present at save time are written to `install_appointment_history.conflicts_acknowledged`.
- **Hard conflict:** none in slice 1. The response type keeps a `"severity": "soft"` field on each conflict (always "soft") so a future working-hours or time-off rule can add `"hard"`. A conflict list containing any hard item would then reject even with acknowledgement.
- Conflict lists are capped at 20 items plus `"moreConflicts": N`.
- **IDOR within conflicts:** conflicting appointments the caller cannot read under own scope are reported as `{type, employeeId, employeeName, startLocal, endLocal}` without `appointmentId` or `jobNumber`. Employee names are already visible through crew-options.

## 7. Time zones

- **Storage:** `starts_at`/`ends_at` are TIMESTAMPTZ (UTC instants). `timezone` stores the IANA zone resolved at write time. `local_start_date`/`local_end_date` are derived in Go.
- **Zone resolution:** `automation.TenantLocation(companyprofile.Get(ctx,pool).Timezone, fallback)`. The fallback `*time.Location` comes from `config.AppConfig.AutomationDefaultTimezone` and is injected into the ops constructor exactly as `custTxTZ` is at main.go:1170-1174.
- **Input conversion** (`installschedule/localtime.go`):
  - `ParseLocal(s string, loc *time.Location) (time.Time, error)` parses `2006-01-02T15:04`. It checks that `t.In(loc)` keeps the same wall clock; if not, the time falls in a DST gap and it returns `ErrNonexistentLocalTime` (→ 400 `invalid_time`).
  - For an ambiguous wall time, Go's `time.Date` picks the first occurrence. This is pinned by test.
- **All-day:** stored as local midnight of the start date to local midnight of the day after the end date, both converted to UTC, with `all_day=true`. A 23- or 25-hour DST day is handled because the bounds are computed per date with `time.Date(y,m,d,0,0,0,0,loc)`, never by adding 24h.
- **Output:** every Appointment carries both UTC (`startsAt`) and local wall-clock (`startLocal`, formatted in the stored `timezone`).
  - The WebUI places events by `startLocal`, which matches the planner's zone-less wall-clock convention in `src/lib/plannerPlacement.ts`. A user whose browser is in another zone still sees tenant time, labelled with the zone abbreviation.
  - Assumption: tenants operate in one zone (consistent with the single-entity policy).
- **Feed window:** `from`/`to` are local dates. The UTC bounds are local midnight of `from` and local midnight of `to+1`. The query is `starts_at < $toUTC AND ends_at > $fromUTC AND starts_at >= $fromUTC - interval '14 days'`, where the last term keeps `idx_ia_live_start` usable thanks to the 14-day max duration. Row cap 2000, plus `truncated`.
- **Tenant today** (backdating rule): computed in Go via `automation.TenantToday(now, tz, fallback)`. No SQL date functions.
- **Changing the tenant zone later** does not rewrite stored instants. Displays use the stored per-row `timezone`. This is documented in the help doc.

## 8. Lifecycle, audit, events, notifications

**States:** `scheduled → completed`, `scheduled → cancelled`. Both are terminal. A cancelled visit is re-booked as a new appointment; there is no un-cancel. There is no hard delete (no `delete` action in the catalog).

**Reschedule** (PATCH with a time change) and **reassign** (PATCH with a crew change) keep the same uuid and bump `record_version`. Each writes a history row with from/to times or a crew diff in the snapshot.

**Workflow interaction:**
- None. Appointment transitions do not call fabrication transitions, and the fabrication `notifyTransition` hook is **not** called, because no fabrication status changes.
- If a fabrication job is cancelled (CANC) or completed, its future scheduled appointments stay as they are in slice 1. The calendar card shows a "Job cancelled" badge, computed from the joined job status, and the scheduler cancels them by hand. Auto-cancel is a follow-up, recorded in Open Questions Q9 as a non-blocking note.

**Domain events** (in-transaction, `domainevent.Emit(ctx, tx, domainevent.Event{...})`, Resource `install_appointment`, RecordID = appointment uuid):
- `install_appointment.scheduled`, `.rescheduled`, `.reassigned`, `.updated`, `.cancelled`, `.completed`
- Payload: `{fabricationJobId, visitType, changed: ["startsAt","crew"], version}`. It carries ids and changed keys only, no notes and no names.
- `Event.Payload` is `any` and is JSON-marshalled. Callers pass typed structs carrying ids (verified in `domainevent/event.go:51-53`). Use a typed `scheduledPayload` struct.

**Audit:** after commit, call `workflow.LogAuditFull(ctx, pool, actorUserID, action, "install_appointment", uuid, "install_appointment", oldVal, newVal, extraMeta, ip, session, appVersion)`, mirroring the fabrication controller's call sites. Errors are logged with slog, never returned.

**History:** written in the same transaction as the change. The `notify_notification_ids` column is filled after the post-commit notify, in a small `UPDATE … WHERE install_appointment_history_id = $1`. A failure there is logged, not returned.

**Notifications:**
- A new helper `approvalchain.NotifyEmployees(ctx context.Context, pool *pgxpool.Pool, ev EmployeesEvent) (services.NotificationResult, error)` in a new file `approvalchain/notify_employees.go`. It resolves `employee_id → users` the same way `actorContact` does, and skips employees without users, logging `slog.Info("install notify skipped: no login", "employee_id", id)`.
- Recipients:

  | change | who is notified |
  |---|---|
  | create | all crew |
  | reschedule | all crew (old ∪ new) |
  | reassign | the added crew ("You've been booked") and the removed crew ("You've been taken off") |
  | cancel | all crew |
  | complete, or a notes-only update | no one |

  The actor is never notified about their own change.
- `ActorUserID` is the caller's user id. `IdempotencyKey` = hex sha256 of `appointmentUUID + ":" + version + ":" + eventType + ":" + recipientsetHash`. `StatusLink` = `/calendar/install-schedule?appointment=<uuid>`. The body uses local time plus a zone abbreviation, e.g. "Tue 10 Nov, 8:00–12:00 CST".
- Assumption: the exact `EventContext`/channel fields needed are copied from `buildActorCreatedRequest`. Read it in Task B6.
- `emailSent` is true iff at least one notification id came back.

## 9. Customer portal read model (designed, not built in slice 1)

Later slice: `GET /api/portal/sales-orders/{uuid}` (main.go:567-601 block) gains `nextVisit: {visitType, localStartDate, window: "morning|afternoon|all day", status}`.
- It is derived from the earliest live appointment on any job of that SO whose `starts_at` is in the future.
- It never exposes crew names, notes or exact minutes.
- It is gated by `portal/visibility.go` with a new module key.

No portal code changes in slice 1 (anti-goal A4).

## 10. WebUI

### 10.1 Route, nav, permissions
- Route `calendar/install-schedule` in `src/router/index.tsx`, next to `calendar/activity-planner` (line 865). Lazy import beside line 382. `PermissionGuard` takes `resource="install_appointment"` and `action="read"`, with no workflowKey.
  - Assumption: PermissionGuard's prop names for resource and action are not verified. Copy the activity-planner usage.
- Nav child under the Calendar group in `src/config/sidebarNav.ts:550-575`: label "Install Schedule", resource `install_appointment`.
  - `buildPermModules` derives the role-matrix row from nav entries, so this also makes `install_appointment` grantable in Roles. Verify the matrix row appears (Task W7 test).
  - The resource must not already appear earlier in nav, since buildPermModules keeps the first occurrence.
- Inside the page:
  - "New visit" and edit controls are shown only with `install_appointment:create` / `:update`.
  - Cancel and Complete require `:transition`.
  - While permissions load, render read-only (not optimistic, because write controls must not flicker in).

### 10.2 Calendar page
`src/pages/calendar/InstallSchedulePage.tsx` (≤ 300 lines). It reuses the URL-param pattern of `ActivityPlannerPage.tsx`: `view`, `date`, plus `crew` (employee id) and `appointment` (open dialog).
- It reuses these components unchanged: `PlannerNavigator`, `PlannerViewSwitch`, `PlannerTimeGrid`, `PlannerMonthGrid`, `PlannerPhoneCalendar`, and `bucketByColumn` / `columnsFor` / `rangeFor` from `src/lib/plannerCalendar.ts`.
- New card kind:
  - Extend `PlannerCard['kind']` in `src/lib/plannerCards.ts` with `'appointment'`.
  - Add the `cardNoun('appointment', n)` text "visit"/"visits".
  - A new mapper `src/lib/installScheduleCards.ts` turns `Appointment` into `PlannerCard`. `at` = `startLocal`; all-day items go in the "All day" row; the chip label is `jobNumber · customerName`; the colour comes from visitType; cancelled cards use line-through and muted colour.
  - `href` = `?appointment=<uuid>` (same page), so the existing `PlannerChip` popover's open link opens the edit dialog.
  - Assumption: `PlannerCard` fields beyond `kind`/`href`/`openLabel`/`at`/`sortKey` must be read in Task W2 and mapped one-for-one.
- Multi-day events show on each day they cover, as one card per covered column, by expanding `localStartDate..localEndDate` in the mapper.
- **Crew filter:** a `<select>` from crew-options, plus "All crew". It sends `employeeId`.
- **States:** loading (`Spinner`), error (`ErrorNote` plus "Try again"), empty ("No visits booked in this view."), truncated banner (same copy pattern as the planner), and `Updating…`.
- **Phone (< 768px):** `PlannerPhoneCalendar` plus a day list. "New visit" is a full-width button and the dialog is full-screen.

### 10.3 Drag-and-drop decision
Not in slice 1. The reasons:
- No DnD library is installed (`package.json`).
- Accessible keyboard DnD plus touch is a significant chunk of work.
- Every move must pass the conflict acknowledgement step anyway.

Rescheduling goes through the dialog, which also serves keyboard and screen-reader users. Revisit with `@dnd-kit/core` after slice 1 telemetry shows reschedule frequency.

### 10.4 Dialog
- `src/pages/calendar/components/InstallAppointmentDialog.tsx`: a hand-rolled `createPortal` dialog like `CancelFabricationJobDialog.tsx`, with `role="dialog"`, `aria-modal`, a labelled title, focus trap, Esc to close, and focus returned to the trigger. It works in create and edit modes.
- Fields:
  - **Job:** a search box using `fabricationService.searchJobs`. It is prefilled and locked when opened from a job.
  - **Visit type:** radio buttons.
  - **Date:** `DatePicker` from `components/ui/date-picker`.
  - **All-day:** a toggle.
  - **Times:** start and duration selects in 15-minute steps. Default durations are template 1h, install 4h, service 2h (Q7).
  - **Crew:** `InstallCrewPicker`, with one lead radio and member checkboxes.
  - **Notes.**
  - **Read-only job context:** promised install date and job crew lead.
- `InstallConflictNotice.tsx` renders the 409 conflicts list in a `role="alert"` region with a "Book anyway" button, which resubmits with `acknowledgeConflicts: true`, and a "Change time" button.
- `InstallCancelDialog.tsx` asks for a required reason.
- A stale version (409 `stale_version`) shows "Someone else changed this visit. Reload to see the latest." with a Reload button that refetches and resets the form.
- `requestId` for create comes from the existing `src/hooks/useIdempotencyKey.ts`, a stable key per form fingerprint.

### 10.5 Fabrication job Visits tab
- `src/pages/sales/fabrication/FabricationVisitsTab.tsx` lists the job's visits with a "Book visit" button that opens the same dialog prefilled.
- Assumption: the folder `src/pages/sales/fabrication/` exists. If not, place the file beside `FabricationJobDetailPage.tsx`.
- It is added to `TABS` in `FabricationJobDetailPage.tsx` only after the W6 split, and shown only with `install_appointment:read`.

### 10.6 Data layer
- `src/types/installSchedule.ts` mirrors §5.2.
- `src/services/installScheduleService.ts` exports `getFeed`, `getAppointment`, `createAppointment`, `updateAppointment`, `cancelAppointment`, `completeAppointment`, `getHistory`, `listForJob` and `getCrewOptions`, using the same tenant client as `salesOrderService.ts`.
- Hooks: `src/hooks/useInstallAppointments.ts` (feed, 60s poll, `placeholderData` like `usePlannerEvents`), `src/hooks/useInstallAppointmentMutations.ts`, `src/hooks/useCrewOptions.ts`.
- Query keys:
  - `['install-appointments','feed',{from,to,employeeId}]`
  - `['install-appointments','detail',uuid]`
  - `['install-appointments','history',uuid]`
  - `['install-appointments','job',jobUuid]`
  - `['install-appointments','crew-options']`
- Every mutation invalidates `['install-appointments']` (prefix) and `['fabrication-job', jobUuid]`.

### 10.7 Accessibility
- Each chip's accessible name reads "Install, J-1042 Smith, Tue 10 Nov 8:00 to 12:00, 2 crew, scheduled".
- The conflict notice is announced via `role="alert"`, and the count line in the header via `aria-live="polite"` (as in the planner).
- All controls are reachable by keyboard. Colour is never the only status signal (cancelled also uses text and line-through).

## 11. Tests and verification

Backend (repo root):
```bash
gofmt -l . && go build ./... && go vet ./...
go test ./installschedule/... ./controllers/... ./authz/... ./approvalchain/...
TEST_DATABASE_URL=… TEST_CP_DATABASE_URL=… go test -tags dbtest ./installschedule/... ./controllers/...
psql "$TEST_DATABASE_URL" --single-transaction -f database/migrations/tenant/schema.sql   # run twice
go run ./cmd/gen-apidocs && go run ./cmd/gen-apidocs -check
golangci-lint run ./installschedule/... ./controllers/...
```
WebUI (repo root): `npm run lint && npm run typecheck && npm test && npm run build`. `npm run ci` runs all four.

Agents to run before merge:
- `migration-auditor` (schema.sql)
- `tenancy-security-reviewer` (installschedule and the controllers)
- `go-verifier` for the consolidated report

`module-drift-checker` is not applicable, since no document module or fabrication file is edited apart from the read-only use. `filter-invariant-checker` is not applicable, since the feed does not use `query/` (window-feed precedent: `salesorder/schedule_feed.go`).

Skills: `add-migration` for Task B1 and `new-tenant-endpoint` for Task B7.

## 12. Docs, apidocs, rollout, risks

**Help docs** (the corpus auto-resyncs on boot when the hash changes; manual fallback `POST /api/platform/ai/reindex-help`):
- New `docs/install-schedule.md`: booking, conflicts and overrides, reschedule/cancel/complete, time zone note, who gets notified, the difference from the Activity Planner.
- Add one sentence plus a link in `docs/fabrication.md` under "## Job site, schedule, and assignment" (line 34).
- Add the Calendar line in `docs/getting-started.md:24`.
- Add a disambiguation sentence in `docs/sales-documents.md:72`: Activity Planner "Installation" is the order plan, while Install Schedule holds crew bookings.

**API docs:** add the routes to `apidocs/` per its existing registration pattern (assumption: read `cmd/gen-apidocs` to find where route docs are declared), then regenerate `docs/openapi.json` and `docs/api-reference.md`.

**Rollout / feature flag:**
- Dark by RBAC. The resource ships with no grants, so only `super_admin` sees the nav item, and tenant admins opt in by granting `install_appointment:*` in Roles.
- No env flag is needed, because tables and routes are inert without grants. This matches Q5's default.
- If the PO wants a kill switch, add `INSTALL_SCHEDULE_ENABLED` following the `DocExtractEnabled` pattern at main.go:915 (routes not registered when off). This is not planned by default.
- Release order: backend (schema plus API) first, then WebUI. The WebUI route 403s gracefully if the backend is older.

**Risks**
1. *Two "installation" calendars confuse users.* The Activity Planner's Installation tab shows SO plan dates, and Install Schedule shows bookings. Mitigation: distinct names, a disambiguation line on both pages and in the docs, and Q8 decides on sync.
2. *Time zone and DST bugs,* and the WebUI's zone-less wall-clock convention. Mitigation: the server always returns `startLocal`, the WebUI never converts, and there are pinned DST tests (Review Focus 1).
3. *Collision with fabrication v2 Tasks 10–12* (files, job version, the "installed" meaning). Mitigation: §3 rules, `installschedule/` is a separate package, and the W6 split is coordinated.
4. *Booking races.* Mitigated by advisory locks plus the concurrent test.
5. *Crew without logins miss notifications.* The slog skip plus `emailSent:false` are surfaced in the dialog ("2 of 3 crew notified").

**300-line split plan (new files, each ≤ 300):**
- `installschedule/`: types.go, localtime.go, validate.go, conflict.go, store_get.go, store_feed.go, store_create.go, store_update.go, store_status.go, history.go, events.go, notify.go, crew_options.go
- `controllers/`: install_schedule.go (ops plus auth plus reads), install_schedule_write.go, install_schedule_errors.go
- Existing over-cap files touched: `FabricationJobDetailPage.tsx` (415 lines) is split first (Task W6). `salesorder/schedule.go` (506 lines) is **not** touched.

## 13. Slice 2: Mobile-friendly job pages with photo capture (high level)

- **Build on, do not duplicate,** workspace-plan Task 11 (`installer_access.go`, `middleware/installer.go`, `InstallerWorkPage.tsx`) and Task 12 (responsive workspace). Slice 2 starts after those land, or is merged into them by agreement with their owner.
- **"My visits today":** an installer landing page fed by `GET /api/tenant/install-appointments?from=today&to=today&employeeId=<self>` under own scope. Each card links to the job's phone page: site address with a map link, a `tel:` link for the site phone, piece list, notes.
- **Photo capture:**
  - `<input type="file" accept="image/*" capture="environment" multiple>` with client-side downscale (canvas, longest edge 2048px, JPEG 0.8).
  - Upload to R2 via the existing `storage/` package with presigned PUT. Assumption: a presign helper exists; verify at slice start.
  - New table `install_visit_photo` (appointment id, job id, R2 key, caption, taken_at, uploaded_by, phase before|during|after) with RBAC action `install_appointment:update` under own scope.
  - EXIF GPS is stripped server-side by default (privacy). A PO decision is needed to keep it.
- **Visit check-in/out:** `started_at`/`finished_at` columns added to `install_appointment` (`ADD COLUMN IF NOT EXISTS`). "Complete" is still the visit, never sign-off.
- **Risks:** large uploads on cellular (resumable chunking deferred), storage cost, and photos of customer homes (retention policy).

## 14. Slice 3: Offline (high level)

- This is out of scope in the fabrication workspace plan ("Offline operation … excluded"), so it needs an explicit PO decision before any work.
- **Scope proposal:** read-only offline for today's visits (cached job page, address, notes), plus a queued photo upload and queued check-in/out. No offline create or reschedule, which avoids conflict resolution.
- **Mechanism:**
  - A service worker (Vite PWA plugin, a new dependency that needs approval) caches the app shell.
  - IndexedDB stores today's visit snapshots and an outbox.
  - Each outbox item carries a `requestId` and `expectedVersion`, so replays are idempotent and stale ones surface as 409 for manual review.
- **Server:** the existing idempotent create path (`create_request_id`) plus a new idempotency key on photo upload. No server-side merge logic.
- **Risks:** stale data shown as current (needs a "last synced" banner), storage eviction on iOS, and auth token expiry while offline.

---

## 15. Slice 1 task table

Model tiers: **S** = Sonnet (`go-implementer` for backend), **H** = Haiku (mechanical), **O** = Opus (review gate only). Sizes: S < 150 LOC, M 150–400, L > 400.

| id | title | repo | files | depends on | tier | size | acceptance check |
|---|---|---|---|---|---|---|---|
| B1 | Schema: 3 tables plus indexes | BE | `database/migrations/tenant/schema.sql` (append) | none | S | S | Applies twice in `--single-transaction` on a fresh DB; `migration-auditor` is clean |
| B2 | Local time and validation (pure) | BE | `installschedule/types.go`, `localtime.go`, `localtime_test.go`, `validate.go`, `validate_test.go` | none | S | M | Table tests pass, including the DST gap/ambiguous cases, 15-min steps, the 14-day cap and crew rules |
| B3 | Store: get, feed, crew options | BE | `installschedule/store_get.go`, `store_feed.go`, `crew_options.go`, `store_feed_dbtest_test.go` | B1, B2 | S | M | The dbtest feed respects the window, own scope (owner or crew), the cap and `truncated` |
| B4 | Store: create plus conflict check | BE | `installschedule/conflict.go`, `store_create.go`, `history.go`, `conflict_test.go`, `store_create_dbtest_test.go` | B3 | S | L | Soft 409 then an acked save; idempotent requestId; the concurrent test produces exactly one clean 201 |
| B5 | Store: update, cancel, complete | BE | `installschedule/store_update.go`, `store_status.go`, `store_update_dbtest_test.go` | B4 | S | M | `stale_version`, `invalid_status`, `not_started`, history rows per action; `fabrication_job_record_version` unchanged |
| B6 | Events, audit, notifications | BE | `installschedule/events.go`, `notify.go`, `approvalchain/notify_employees.go`, `approvalchain/notify_employees_test.go` | B5 | S | M | Emit uses the tx; idempotency key is stable; a no-login employee is skipped and `emailSent` is false |
| B7 | RBAC resource, controllers, routes | BE | `authz/catalog.go`, `authz/catalog_test.go`, `controllers/install_schedule.go`, `install_schedule_write.go`, `install_schedule_errors.go`, `install_schedule_dbtest_test.go`, `main.go`, route guard tests | B6 | S | L | The IDOR test returns 404 plus `idor_denied`; 403 without the grant; create without installation:read on the job returns 404; `tenancy-security-reviewer` is clean |
| B8 | Docs and apidocs | BE | `docs/install-schedule.md`, `docs/fabrication.md`, `docs/getting-started.md`, `docs/sales-documents.md`, `apidocs/*` | B7 | H | S | `go run ./cmd/gen-apidocs -check` passes; the docs guard tests pass |
| W1 | Types, service, hooks | WEB | `src/types/installSchedule.ts`, `src/services/installScheduleService.ts`, `src/hooks/useInstallAppointments.ts`, `useInstallAppointmentMutations.ts`, `useCrewOptions.ts`, plus tests | B7 (contract) | S | M | Hook tests cover the keys and the invalidation prefix, using `vi.mock` of the service |
| W2 | Card kind plus mapper | WEB | `src/lib/plannerCards.ts`, `src/lib/installScheduleCards.ts`, `installScheduleCards.test.ts` | W1 | S | S | Multi-day expands per column; all-day goes to the all-day row; cancelled styling; Activity Planner tests still pass |
| W3 | Install Schedule page | WEB | `src/pages/calendar/InstallSchedulePage.tsx`, `InstallSchedulePage.test.tsx` | W2 | S | M | Loading, error, empty and truncated states; crew filter in the URL; phone layout under 768px |
| W4 | Appointment dialog plus conflict UI | WEB | `components/InstallAppointmentDialog.tsx`, `InstallCrewPicker.tsx`, `InstallConflictNotice.tsx`, `InstallCancelDialog.tsx`, plus tests | W3 | S | L | 409 conflict → "Book anyway" resends with ack; stale → reload; focus trap and Esc; permission-gated buttons |
| W5 | Route, nav, role matrix | WEB | `src/router/index.tsx`, `src/config/sidebarNav.ts`, nav test | W3 | H | S | The route is guarded by install_appointment:read; the nav item is hidden without it; the Roles matrix lists the resource |
| W6 | Split FabricationJobDetailPage | WEB | `src/pages/sales/FabricationJobDetailPage.tsx` → extract the tab bodies | none | S | M | Behaviour is unchanged (existing tests pass); the file is ≤ 300 lines |
| W7 | Visits tab on the job | WEB | `FabricationVisitsTab.tsx`, `FabricationJobDetailPage.tsx` (TABS), test | W4, W6 | S | S | The tab lists visits; "Book visit" prefills the job; hidden without the read permission |
| R1 | Branch review gate | both | none | all | O | none | `go-verifier` report and `npm run ci` are green; the review finds no high issues |

### Task B1: Schema

**Files:** Modify `database/migrations/tenant/schema.sql` (append at EOF).

- [ ] Step 1: Invoke the `add-migration` skill.
- [ ] Step 2: Append the three tables and indexes exactly as in §4.2. Name the constraints `chk_ia_*`, `chk_iac_*` and `chk_iah_*`.
- [ ] Step 3: Apply to a fresh DB twice: `psql "$TEST_DATABASE_URL" --single-transaction -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql`. Expected: both runs succeed.
- [ ] Step 4: Run the `migration-auditor` agent. Expected: no findings.
- [ ] Step 5: Commit `feat(installschedule): add install appointment tables`.

### Task B2: Local time and validation

**Files:** Create `installschedule/types.go`, `localtime.go`, `localtime_test.go`, `validate.go`, `validate_test.go`.

**Interfaces (Produces):**
- `type VisitType string` with constants `VisitTemplate="template"`, `VisitInstall="install"`, `VisitService="service"`. `type Status string` with constants `StatusScheduled`, `StatusCompleted`, `StatusCancelled`. `type CrewRole string` with `CrewLead="lead"`, `CrewMember="member"`.
- `type CrewInput struct{ EmployeeID int; Role CrewRole }`
- `type CreateInput struct{ FabricationJobUUID string; VisitType VisitType; StartLocal, EndLocal string; AllDay bool; Crew []CrewInput; Notes string; RequestID string; AcknowledgeConflicts bool }`
- `type UpdateInput struct{ StartLocal, EndLocal *string; AllDay *bool; Crew *[]CrewInput; Notes *string; VisitType *VisitType; ExpectedVersion int; AcknowledgeConflicts bool; Reason string }`
- `type Interval struct{ Start, End time.Time; AllDay bool; LocalStartDate, LocalEndDate dateonly.Date; TZ string }`
  - `dateonly.Date` is a verified struct at `dateonly/dateonly.go:23`.
- `type Appointment struct` with JSON tags per §5.2. `type CrewMember struct{ EmployeeID int; Name string; Role CrewRole }`.
- Sentinel errors: `ErrNotFound`, `ErrStaleVersion`, `ErrInvalidStatus`, `ErrNotStarted`, `ErrJobClosed`, `ErrNonexistentLocalTime`, and `type ValidationError struct{ Field, Message string }`, plus `type ConflictError struct{ Conflicts []Conflict; More int }` and `type Conflict struct{ Type string; Severity string; EmployeeID int; EmployeeName string; AppointmentUUID string; JobNumber string; StartLocal, EndLocal string }`.
- `func ParseLocal(s string, loc *time.Location) (time.Time, error)`
- `func ResolveInterval(startLocal, endLocal string, allDay bool, loc *time.Location) (Interval, error)`
- `func ValidateCreate(in CreateInput, today time.Time) error`, `func ValidateCrew(crew []CrewInput) error`
- Constants: `MaxDuration = 14*24*time.Hour`, `SlotMinutes = 15`, `MaxCrew = 10`, `MaxNotes = 2000`, `MaxReason = 500`, `MaxFeedDays = 93`, `FeedRowCap = 2000`, `ConflictListCap = 20`.

- [ ] Step 1: Write the table test `TestParseLocal` with `loc = time.LoadLocation("America/Chicago")`:
  - `"2026-11-10T08:00"` → `2026-11-10T14:00:00Z`
  - `"2026-03-08T02:30"` → `errors.Is(err, ErrNonexistentLocalTime)`
  - `"2026-11-01T01:30"` → `2026-11-01T06:30:00Z` (the first occurrence, CDT)
  - `"2026-11-10T08:00:00"` → error (wrong layout)
  - `""` → error
- [ ] Step 2: Write the table test `TestResolveInterval`:
  - all-day `2026-03-08`..`2026-03-08` → End-Start == 23h, LocalStartDate == LocalEndDate == 2026-03-08
  - all-day `2026-11-01`..`2026-11-02` → 49h
  - timed 08:00–08:00 → ValidationError on `endLocal`
  - timed span of 14 days plus 15 minutes → ValidationError
  - minute `08:10` → ValidationError ("15-minute steps")
- [ ] Step 3: Write the table test `TestValidateCrew`. Rejected cases: two leads, a duplicate employee, 11 entries, `EmployeeID<=0`, an unknown role. Accepted cases: an empty list, and members only.
- [ ] Step 4: Write the table test `TestValidateCreate`. Rejected cases: a start before today−1 day, notes of 2001 chars, an unknown visitType, a missing job uuid.
- [ ] Step 5: Run `go test ./installschedule/ -run 'TestParseLocal|TestResolveInterval|TestValidate' -v`. Expected: FAIL (undefined).
- [ ] Step 6: Implement. In `ParseLocal`, use `time.ParseInLocation("2006-01-02T15:04", s, loc)`, then reject with `ErrNonexistentLocalTime` if `t.Hour()/t.Minute()` differ from the parsed wall clock. All-day bounds use `time.Date(y,m,d,0,0,0,0,loc)` and `AddDate(0,0,1)`.
- [ ] Step 7: Re-run. Expected: PASS. Commit `feat(installschedule): local time resolution and validation`.

### Task B3: Store reads

**Files:** Create `installschedule/store_get.go`, `store_feed.go`, `crew_options.go`, `store_feed_dbtest_test.go` (`//go:build dbtest`, with a `testPool(t)` copied from `fabrication/store_delete_test.go:18`).

**Interfaces (Produces):**
- `type Querier interface{ QueryRow(...); Query(...); Exec(...) }`, defined at the point of use. Alternatively use `*pgxpool.Pool` and `pgx.Tx` directly, as fabrication does.
- `func Get(ctx context.Context, q Querier, uuid string) (*Appointment, error)` returns `ErrNotFound`.
- `func OwnerAndCrew(ctx context.Context, q Querier, uuid string) (jobOwnerUserID string, crewUserIDs []string, err error)` is used for the IDOR check.
  - Owner = `fabrication_job.job_owner_id` resolved to a users id the same way `fabrication/store.go` resolves own scope. Crew = `employee.employee_user_id`.
  - Assumption: the exact join from job_owner_id to an identity id is copied from `fabrication/store_search.go`.
- `type FeedRequest struct{ From, To string; EmployeeID int; VisitType VisitType; IncludeCancelled bool }`
- `func Feed(ctx context.Context, q Querier, loc *time.Location, scope string, actorIdentityID string, req FeedRequest) (items []Appointment, truncated bool, err error)`
- `func ListForJob(ctx context.Context, q Querier, jobUUID string) ([]Appointment, error)`
- `type CrewOption struct{ EmployeeID int; Name string; HasLogin bool }`. `func CrewOptions(ctx context.Context, q Querier) ([]CrewOption, error)` returns active, non-system, non-deleted employees, using `employee_is_active`, `employee_is_system` and `employee_deleted_at` (verified at schema.sql:514).

Own-scope SQL predicate: `(job owner = actor) OR EXISTS (crew row whose employee_user_id = actor)`.

- [ ] Step 1: Write the dbtest `TestFeed_WindowAndScope`. Seed a job via a local `seedSalesOrder` copy and a direct `fabrication_job` insert, or via `fabrication.Create`. Insert three appointments: inside the window, outside the window, and cancelled.
  - Assert the feed returns one item.
  - With `IncludeCancelled`, assert it returns two.
  - Under own scope as an unrelated identity, assert it returns zero.
  - Under own scope as a crew member's user, assert it returns one.
- [ ] Step 2: Write the dbtest `TestFeed_Truncates`. With `FeedRowCap` overridden through an unexported var, assert `truncated == true`.
- [ ] Step 3: Write the dbtest `TestCrewOptions_ExcludesSystemAndInactive`, asserting employee id 1 (system) is absent.
- [ ] Step 4: Run `go test -tags dbtest ./installschedule/ -run 'TestFeed|TestCrewOptions' -v`. Expected: FAIL.
- [ ] Step 5: Implement per §7 (window SQL) and §5.2 (field mapping). `startLocal` is formatted from the row's `timezone`.
- [ ] Step 6: Re-run (PASS) and commit `feat(installschedule): appointment reads and window feed`.

### Task B4: Create plus conflicts

**Files:** Create `installschedule/conflict.go`, `store_create.go`, `history.go`, `conflict_test.go`, `store_create_dbtest_test.go`.

**Interfaces (Produces):**
- `func Overlaps(a, b Interval) bool` (pure, half-open)
- `func lockScope(ctx context.Context, tx pgx.Tx, jobID int, employeeIDs []int) error`, which sorts ascending and dedupes
- `func findConflicts(ctx context.Context, tx pgx.Tx, jobID int, iv Interval, employeeIDs []int, excludeApptID int, canSee func(apptUUID string) bool) ([]Conflict, int, error)`
- `func writeHistory(ctx context.Context, tx pgx.Tx, h HistoryRow) (int64, error)` and `func SetHistoryNotifyIDs(ctx context.Context, q Querier, historyID int64, ids []string) error`
- `type CreateResult struct{ Appointment *Appointment; HistoryID int64; Replayed bool }`
- `func Create(ctx context.Context, pool *pgxpool.Pool, loc *time.Location, in CreateInput, actorEmployeeID int, canSee func(string) bool) (*CreateResult, error)` returns a `*ConflictError` on unacknowledged overlap.

- [ ] Step 1: Write the table test `TestOverlaps`:
  - 8–12 vs 12–16 → false
  - 8–12 vs 11–13 → true
  - all-day 10 Nov vs 10 Nov 08:00–09:00 → true
  - identical → true
- [ ] Step 2: Write the dbtest `TestCreate_SoftConflictThenAck`:
  - Book employee E 08–12.
  - A second booking 10–14 with E returns `*ConflictError` with one crew conflict, Severity "soft".
  - The same input with `AcknowledgeConflicts: true` succeeds, and the history row has non-null `conflicts_acknowledged` containing the first uuid.
- [ ] Step 3: Write the dbtest `TestCreate_IdempotentRequestID`. The same RequestID twice returns the same uuid, `Replayed == true` on the second call, and only one row exists.
- [ ] Step 4: Write the dbtest `TestCreate_ConcurrentSameCrew`. Twenty goroutines run with a `sync.WaitGroup`, each booking E 08–12 with no ack. Exactly one succeeds and nineteen return `*ConflictError`.
- [ ] Step 5: Write the dbtest `TestCreate_ClosedJob`. A job with status CANC returns `ErrJobClosed`.
- [ ] Step 6: Write the dbtest `TestCreate_DoesNotTouchJobVersion`. Read `fabrication_job_record_version` before and after creating; it is equal.
- [ ] Step 7: Run and see FAIL. Implement in one transaction:
  1. Resolve the job id and status (live, not COMP/CANC).
  2. Run the idempotency lookup by `(fabrication_job_id, create_request_id)`.
  3. Validate employees.
  4. Lock (§6).
  5. Find conflicts.
  6. Insert the appointment and crew.
  7. Write history.
  8. Commit.

  Emit and notify are not wired yet; they come in B6.
- [ ] Step 8: Re-run (PASS) and commit `feat(installschedule): create with conflict detection`.

### Task B5: Update, cancel, complete

**Files:** Create `installschedule/store_update.go`, `store_status.go`, `store_update_dbtest_test.go`.

**Interfaces (Produces):**
- `type ChangeResult struct{ Before, After *Appointment; HistoryIDs []int64; Actions []string; AddedCrew, RemovedCrew []int }`
- `func Update(ctx context.Context, pool *pgxpool.Pool, loc *time.Location, uuid string, in UpdateInput, actorEmployeeID int, canSee func(string) bool) (*ChangeResult, error)`
- `func Cancel(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, reason string, actorEmployeeID int) (*ChangeResult, error)`
- `func Complete(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, actorEmployeeID int, now time.Time) (*ChangeResult, error)`

The version check is an `UPDATE … WHERE install_appointment_id=$1 AND record_version=$2 RETURNING …`. Zero rows means `ErrStaleVersion`, and the current version is read for the error.

- [ ] Step 1: Write the dbtest `TestUpdate_StaleVersion`. Two updates with `ExpectedVersion: 1`: the second gets `ErrStaleVersion`.
- [ ] Step 2: Write the dbtest `TestUpdate_RescheduleAndReassign`:
  - Change the time and swap a crew member in one call.
  - Assert `Actions == ["reschedule","reassign"]`, two history rows, `AddedCrew`/`RemovedCrew` correct, and the version bumped by exactly 1.
  - The conflict check excludes the appointment's own id.
- [ ] Step 3: Write the dbtest `TestCancel_RequiresScheduled`. Cancel then cancel again: the second returns `ErrInvalidStatus`. Cancelling with an empty reason is a ValidationError.
- [ ] Step 4: Write the dbtest `TestComplete_NotStarted`. Completing a future appointment with `now` before `starts_at` returns `ErrNotStarted`.
- [ ] Step 5: Write the dbtest `TestCancelled_DoesNotConflict`. After cancelling, a new overlapping booking for the same crew succeeds without ack.
- [ ] Step 6: Implement, re-run (PASS) and commit `feat(installschedule): reschedule, reassign, cancel, complete`.

### Task B6: Events, audit, notifications

**Files:** Create `installschedule/events.go`, `installschedule/notify.go`, `approvalchain/notify_employees.go`, `approvalchain/notify_employees_test.go`. Modify `store_create.go`, `store_update.go` and `store_status.go` to call `emitEvent(ctx, tx, …)` before commit.

**Interfaces (Produces):**
- `func emitEvent(ctx context.Context, tx pgx.Tx, eventType string, a *Appointment, changed []string, actorUserID string) error`. Its event-type constants are `EventScheduled = "install_appointment.scheduled"`, `EventRescheduled`, `EventReassigned`, `EventUpdated`, `EventCancelled`, `EventCompleted`.
- `approvalchain.EmployeesEvent{ EventType, Resource, RecordUUID, ActorUserID, Title, Body, StatusLink string; EmployeeIDs []int; IdempotencyKey string }`
- `func NotifyEmployees(ctx context.Context, pool *pgxpool.Pool, ev EmployeesEvent) (services.NotificationResult, error)`
- `func NotifyChange(ctx context.Context, pool *pgxpool.Pool, res *ChangeResult, actorUserID string, actorEmployeeID int) (emailSent bool, ids []string)`, plus a `NotifyCreate` twin. This is the recipient matrix from §8; the actor is excluded.
- `func IdempotencyKey(apptUUID string, version int, eventType string, recipients []int) string` (pure)

Domain events take the store's `CreateInput.ActorUserID`. Add an `ActorUserID string` field to `CreateInput`/`UpdateInput` and to the Cancel/Complete parameters if Emit needs it. Update the B4/B5 signatures consistently in this task.

- [ ] Step 1: Write the table test `TestIdempotencyKey`. The output is stable for the same input, changes when the version changes, and is order-insensitive for recipients.
- [ ] Step 2: Write the table test `TestRecipients` in `installschedule/notify_test.go` (pure `recipients(res *ChangeResult, actorEmployeeID int) map[string][]int`):
  - create → all crew minus the actor
  - reassign → `added` / `removed` keys
  - complete → empty
- [ ] Step 3: Write the dbtest `TestNotifyEmployees_SkipsNoLogin`. One crew member has `employee_user_id NULL`. The function returns no error and the request targets exactly the one linked user. Stub the notify sender via the existing services test seam (assumption: read `services/notify.go` for its test hook; if none exists, test only the recipient resolution function `resolveEmployeeContacts`).
- [ ] Step 4: Write the dbtest `TestCreate_EmitsDomainEvent`. After Create, one `domain_event` row (table verified at schema.sql:10021) has type `install_appointment.scheduled`, and it is rolled back if the insert fails.
- [ ] Step 5: Implement, re-run (PASS) and commit `feat(installschedule): domain events and crew notifications`.

### Task B7: RBAC, controllers, routes

**Files:**
- Modify `authz/catalog.go` (add `ResourceInstallAppointment = "install_appointment"` with actions create/read/update/transition, near `ResourceInstallation` lines 299-303) and `authz/catalog_test.go`.
- Create `controllers/install_schedule.go`, `install_schedule_write.go`, `install_schedule_errors.go`, `install_schedule_dbtest_test.go`.
- Modify `main.go` (routes) and the route guard tests (`route_reachability_test.go`, `body_cap_routes_test.go`).

**Interfaces (Produces):**
- `type InstallScheduleOps struct{ pool-resolver deps as FabricationOps; fallbackTZ *time.Location }` and `func NewInstallScheduleOps(…, fallbackTZ *time.Location) *InstallScheduleOps`. Copy the FabricationOps constructor dependencies.
- Handlers `Feed`, `CrewOptions`, `Create`, `Get`, `Update`, `Cancel`, `Complete`, `History`, `ListForJob`, each `func (h *InstallScheduleOps) X(w http.ResponseWriter, r *http.Request)`.
- `func (h *InstallScheduleOps) authAppt(w, r, action string) (uuid string, ok bool)`: permission check, then `OwnerAndCrew`, then `recordInScope` against the owner, OR any crew user equals the identity. On denial it returns 404 and calls `logSecurityEvent(r, "idor_denied", "resource", "install_appointment", "record", uuid)`.
- `func writeInstallError(w http.ResponseWriter, err error)` maps the sentinels and `*ConflictError` to §5.4.

Create also runs a second-resource check modelled on `authSourceSalesOrder`. It requires `installation:read`, then loads the job via `fabrication.Get(ctx,pool,uuid)` and runs `recordInScope` on `job.OwnerUserID`; it returns 404 if the caller cannot see the job.

Actor resolution uses `resolveEmployeeID(r, identityID)` (controllers/crm_admin.go:233). A zero falls back to the system employee id 1, as in fabrication `SoftDelete`.

Body cap: use the same `http.MaxBytesReader` limit as the fabrication handlers.

- [ ] Step 1: Write the table test in `authz/catalog_test.go`. `install_appointment` exists with exactly {create, read, update, transition}, and `delete` is not valid.
- [ ] Step 2: Write the dbtest `TestInstallSchedule_IDOR`, using the `customer_transactions_dbtest_test.go` harness (`withTestJWTSecret`, `newSAMLTestControlPlane`, `seedRBACTestIdentity`, `authz.CreateRole`/`AssignRole`, `tenancy.NewResolver(cp, tenancy.NewRouter(nil)).Middleware(handler)`):
  - (a) An own-scope identity that is neither owner nor crew: GET `/{uuid}` and GET `/{uuid}/history` → 404.
  - (b) The same identity added as crew → 200.
  - (c) No grant → 403.
  - (d) Create on a job the caller cannot see → 404.
  - (e) A PATCH conflict → 409 with `requiresAcknowledgement: true`.
- [ ] Step 3: Write the dbtest `TestInstallSchedule_Feed_BadWindow`. `to` before `from` → 400. A 94-day window → 400.
- [ ] Step 4: Run and see FAIL. Implement. Wire the routes in `main.go` beside the fabrication block, inside `tenantChain`, with `crew-options` before `{uuid}`, and pass the fallback zone like `custTxTZ`. Writes call `LogAuditFull` after commit (slog on error) and `NotifyCreate`/`NotifyChange`, then `SetHistoryNotifyIDs`.
- [ ] Step 5: Run `go build ./... && go vet ./... && go test ./... && go test -tags dbtest ./controllers/ -run InstallSchedule`. Expected: PASS.
- [ ] Step 6: Run the `tenancy-security-reviewer` agent on the diff. Expected: no findings. Commit `feat(installschedule): RBAC resource, endpoints and routes`.

### Task B8: Docs and apidocs

- [ ] Step 1: Write `docs/install-schedule.md` per §12 and edit the three existing docs at the lines cited.
- [ ] Step 2: Add the route docs in `apidocs/` following the existing registration pattern, then run `go run ./cmd/gen-apidocs`.
- [ ] Step 3: Run `go run ./cmd/gen-apidocs -check && go test ./apidocs/... ./docs/...`. Expected: PASS. Commit `docs: install schedule help and API reference`.

### Task W1: Types, service, hooks

**Interfaces (Produces):**
- `Appointment`, `CrewMember`, `Conflict`, `ConflictResponse`, `CreateAppointmentInput`, `UpdateAppointmentInput`, `CrewOption` in `src/types/installSchedule.ts`, mirroring §5.2 field names exactly.
- `installScheduleService` functions per §10.6.
- `isConflictError(err: unknown): err is AxiosError<ConflictResponse>` and `isStaleError(err)` in the service file.
- `useInstallAppointments(range: {from: string; to: string}, employeeId: number | null, enabled: boolean)` returns `{appointments, truncated, isLoading, isFetching, isPlaceholderData, error, refetch}`.
- `useInstallAppointmentMutations()` returns `{create, update, cancel, complete}` (TanStack mutations).
- `INSTALL_APPOINTMENTS_KEY = 'install-appointments'`.

- [ ] Step 1: Write the test `useInstallAppointments.test.tsx` with `vi.mock('@/services/installScheduleService')`. It calls `getFeed` with from/to/employeeId, and the key includes the employeeId.
- [ ] Step 2: Write the test `useInstallAppointmentMutations.test.tsx`. A successful create invalidates the queries with prefix `['install-appointments']` and `['fabrication-job', jobUuid]` (spy on `queryClient.invalidateQueries`).
- [ ] Step 3: Implement, run `npm test -- installSchedule useInstallAppointment` (PASS), then commit.

### Task W2: Card kind plus mapper

- [ ] Step 1: Read `src/lib/plannerCards.ts` and record every `PlannerCard` field.
- [ ] Step 2: Write the test `installScheduleCards.test.ts` for `appointmentsToCards(items: Appointment[]): PlannerCard[]`:
  - A 3-day visit → three cards with the same id prefix on successive dates.
  - An all-day visit → placed on the date-only row (whatever the existing PlannerCard representation is).
  - A cancelled visit → its status field marks it cancelled.
  - `href === '?appointment=<uuid>'`.
- [ ] Step 3: Extend `kind` with `'appointment'` and add the `cardNoun` text "visit"/"visits". Run the existing planner tests: `npm test -- planner`. Expected: PASS, unchanged.
- [ ] Step 4: Implement the mapper, run (PASS) and commit.

### Task W3: Install Schedule page

- [ ] Step 1: Write `InstallSchedulePage.test.tsx` (MemoryRouter plus QueryClientProvider plus mocked service, as in `ActivityPlannerPage.test.tsx`), covering:
  - loading spinner
  - error with Try again calling refetch
  - empty copy "No visits booked in this view."
  - truncated banner
  - changing the crew select writes `?crew=12`
  - "New visit" hidden without `install_appointment:create` (mock `useUserPermissions`)
- [ ] Step 2: Implement the page by composing the reused planner components (§10.2). The dialog is a placeholder here and is filled in W4.
- [ ] Step 3: `npm test -- InstallSchedulePage` (PASS), `npm run typecheck`, then commit.

### Task W4: Dialog plus conflict UI

**Interfaces (Produces):**
- `<InstallAppointmentDialog mode="create"|"edit" appointmentId?: string jobId?: string onClose={() => void} />` (≤ 5 props)
- `<InstallConflictNotice conflicts onBookAnyway onChangeTime />`
- `<InstallCrewPicker value onChange options />`
- `<InstallCancelDialog appointment onClose />`

- [ ] Step 1: Write tests covering:
  - Submit with the mocked `createAppointment` rejecting with a conflict 409 → the notice lists the names; "Book anyway" calls create again with `acknowledgeConflicts: true` and the same `requestId`.
  - Stale 409 on edit → the reload message, and Reload refetches the detail.
  - Esc closes and focus returns to the trigger.
  - The Cancel button is disabled until a reason is entered.
  - `emailSent: false` with crew → the toast/text reads "Saved. Some crew have no login and were not notified."
  - Switching visit type to template sets the duration to 1h.
- [ ] Step 2: Implement, run (PASS) and commit.

### Task W5: Route, nav, role matrix

- [ ] Step 1: Write the test that `buildPermModules` output includes `install_appointment`. Find the existing sidebarNav test or create `sidebarNav.test.ts` beside it.
- [ ] Step 2: Add the lazy route (guarded) and the nav child per §10.1. Run `npm run lint && npm run typecheck && npm test`. Commit.

### Task W6: Split FabricationJobDetailPage

- [ ] Step 1: Run the existing tests for the page and record that they pass.
- [ ] Step 2: Extract tab bodies or header sections into sibling components until the page is ≤ 300 lines, with no behaviour change. Tell the fabrication workspace Task 12 owner about the new file layout in the PR description.
- [ ] Step 3: Re-run the tests (PASS) and `npm run build`. Commit `refactor: split fabrication job detail page`.

### Task W7: Visits tab

- [ ] Step 1: Write the test `FabricationVisitsTab.test.tsx`. It lists two visits from mocked `listForJob`, "Book visit" opens the dialog with the job locked, and it renders nothing without `install_appointment:read`.
- [ ] Step 2: Implement it and add it to `TABS`. Run `npm run ci` (PASS) and commit.

### Task R1: Review gate
- [ ] Run `go-verifier` (backend) and `npm run ci` (WebUI), then do the whole-branch review on Opus. Fix the high and medium findings with `go-implementer` in at most 2 loops.

---

## 16. Open questions for the product owner

| # | Question | Recommended default |
|---|---|---|
| Q1 | Can a visit be booked with no crew yet? | Yes. It shows as "Unassigned" and conflicts are checked on the job only. |
| Q2 | Is crew double-booking a hard block? | No. It is a warning the scheduler must acknowledge, and the override is recorded in history. |
| Q3 | Do we model working hours, time off or crew availability in slice 1? | No. The conflict model keeps a `severity` field so a later "hard" rule can be added. |
| Q4 | Are customers notified (email/SMS) of bookings and changes in slice 1? | No, crew only. A customer "next visit" on the portal comes in a later slice (§9). |
| Q5 | Who may book and reschedule, and do existing roles get access automatically? | Holders of `install_appointment:create/update`. No automatic backfill (dark launch); admins grant it in Roles. |
| Q6 | Are multi-day visits allowed? | Yes, up to 14 days. All-day visits are supported. |
| Q7 | Time granularity and default durations? | 15-minute steps. Defaults: template 1h, install 4h, service 2h. |
| Q8 | Should bookings update the sales order's planned install (Activity Planner "Installation" tab)? | Not in slice 1. The two stay separate, with a disambiguation note on both pages. |

Non-blocking note Q9: when a fabrication job is cancelled, should its future visits auto-cancel? Default: no, they are flagged "Job cancelled" on the card for manual action.

## 17. Jira summary

**Deliverable:** Install Schedule (slice 1 of Field and installation tooling). A crew calendar for booking fabrication job visits, with conflict warnings, audit trail and crew notifications.

**Description:** Schedulers currently have only single planned dates on the sales order and job, with no way to book a crew or see clashes. This adds visit appointments (template, install, service) linked to fabrication jobs, with one lead and optional crew, shown on a week/day/month calendar and a phone view. Double-bookings are warned about before saving and can be overridden with acknowledgement. Visits can be rescheduled, reassigned, cancelled with a reason, and completed. Every change is recorded and notifies affected crew who have logins. The feature ships dark behind a new `install_appointment` permission and does not change fabrication job status or data.

**Requirements:**
1. New tenant tables `install_appointment`, `install_appointment_crew`, `install_appointment_history` (append-only schema).
2. New RBAC resource `install_appointment` (create, read, update, transition) with all/own scope. Own scope means job owner or crew member.
3. Endpoints under `/api/tenant/install-appointments` per §5.1, all behind auth plus TenantResolver, returning 404 on out-of-scope single records.
4. Times are stored in UTC and entered and shown in the tenant time zone. DST gaps are rejected; ambiguous times resolve to the first occurrence.
5. Soft conflict warning for crew and same-job overlaps, safe under concurrent bookings.
6. Optimistic versioning on edits, idempotent create.
7. History, audit log and domain events for every change. Crew notifications with idempotency keys.
8. Calendar › Install Schedule page, booking dialog, and a Visits tab on the fabrication job.
9. Help doc and API reference updated.

**Acceptance criteria**
- **Given** a scheduler with `install_appointment:create` and a live fabrication job, **when** they book an install visit on Tue 10 Nov 08:00–12:00 with a lead installer, **then** the visit appears on the calendar at 08:00–12:00 tenant time, a history "create" row exists, and the lead (if they have a login) receives a notification.
- **Given** an installer already booked 08:00–12:00, **when** a scheduler books them 10:00–14:00 the same day, **then** the save is refused with a warning naming the existing booking, and **when** the scheduler chooses "Book anyway", **then** the visit is saved and the override is recorded in history.
- **Given** two schedulers booking the same installer at the same time simultaneously, **when** both save without acknowledgement, **then** exactly one succeeds and the other sees the conflict warning.
- **Given** a visit at version 3 edited in another tab, **when** a scheduler saves changes based on version 2, **then** they see "Someone else changed this visit" and nothing is overwritten.
- **Given** the tenant zone America/Chicago, **when** a scheduler enters 02:30 on 8 Mar 2026, **then** the save is rejected with a message that the time does not exist on that date.
- **Given** a user with own-scope read who is neither the job owner nor on the crew, **when** they request that visit by its id, **then** they receive 404 and a security event is logged.
- **Given** a visit is cancelled with a reason, **when** the calendar reloads, **then** the visit is shown as cancelled (or hidden unless "show cancelled" is on), its crew are free for new bookings without a warning, and the crew are notified.
- **Given** any booking, reschedule, cancel or complete, **when** the fabrication job is reloaded, **then** its status, crew lead, promised and actual install dates and record version are unchanged.
- **Given** a user without `install_appointment:read`, **when** they open the app, **then** the Install Schedule nav item and the job Visits tab are not shown, and the route is blocked.

# StoneSuite Automation Engine — Design Spec

**Date:** 2026-10-14 · **Status:** Draft — awaiting review
**Repos touched:** `StoneSuite-Backend` (primary), `stonesuite-notify` (Idempotency-Key), Cloudflare Worker (tick cron), `StoneSuite-WebUI` (P1 delivery log/settings, P2 rule builder)
**Related:** `docs/superpowers/specs/2026-10-01-resend-delivery-status-design.md` (bounce/complaint status source)

## Context
Users chase money and deals by hand today. Nothing reminds anyone about an invoice that's due or overdue, a quote that's about to expire, or an approval that's been stuck for days. There's also no way for a tenant to say "when X happens, email Y." The codebase has **no scheduler, cron, dunning or recurring logic**. It does already have most of the building blocks:

- `async_jobs`, a Postgres queue that claims jobs with `SKIP LOCKED` (`jobqueue/jobqueue.go:73,104`).
- A notify client for `email` and `in_app` (`services/notify.go:112`).
- An audit hook, `workflow.AuditObserver` (`workflow/audit.go:14`). It has **one slot, already used by RAG**.
- Signed public PDF links (`controllers/document_link.go`, `DocLinkURL`).
- `company_profile.timezone` (tenant S:8378), which exists but nothing uses it yet.

**Every Fly environment scales to zero**, so an in-process ticker can't be the scheduler.

**Goal:** a secure, low-cost, reliable automation engine. Built-in collections, deals and ops automations come first; tenant-defined rules and the AI features run on the same engine later.

(S = `database/migrations/tenant/schema.sql`)

## Decisions (agreed)
| Topic | Decision |
|---|---|
| Placement | New `automation/` package in the monolith, using `async_jobs`. **No new service.** It talks to modules only through a registry, so it can be extracted later. |
| Scheduler | A Cloudflare Cron Trigger runs every 15 min and calls a signed `POST /api/internal/automation/tick`. |
| Recipients / channels | Internal users and external customers or vendors, by email and in-app. |
| Autonomy | Notify-only in v1. Data-changing actions come later, through the system actor. |
| Permissions | New RBAC resource `automation`. |
| AI | Natural-language rule builder, AI-drafted message copy, smart suggestions. All on Ollama, all advisory. |
| Timezone | Read `company_profile.timezone`, validated with `time.LoadLocation`. Fall back to `America/Chicago` if it's blank or invalid. |
| Built-in defaults | Internal-recipient rules start **on**. External-recipient rules start **off**; an admin must preview and then enable them. |
| System actor | A seeded "StoneSuite Automation" user per tenant. It can't log in, can't be deleted, and is hidden from lists and pickers. |

## How proven systems do it (and what we copy)
- **Salesforce time-based workflow:** a queued timed action is *re-checked when it fires*. It's removed if the record no longer qualifies, and re-queued if the date it's based on changes. **We copy this:** each scheduled send is tied to a specific anchor date, and every send re-checks its conditions at fire time.
- **HubSpot workflows:** explicit enrollment, unenrollment when criteria stop matching, goal-based exits, and re-enrollment turned off by default. **We copy:** goal/stop conditions (paid, converted, cancelled), plus a clear rule for whether a record can trigger the same rule again.
- **Novu digest:** a keyed time window that batches many events into one message. **We copy:** a customer with 5 overdue invoices gets **one** email listing all 5, not 5 emails.
- **Postgres outbox literature:** the honest guarantee is **at-least-once with idempotent effects**, using a lease-based state machine and a reconciliation step for the case where a crash happens after the send. Stripe-style idempotency keys pass that protection through to the provider. **We copy all of it.**
- **Stripe, HubSpot and Chargebee dunning cadences:** about −3d, 0d, +7d, +14d, then a final notice. That becomes our default AR cadence, and each tenant can edit it.
- **Zapier, Workato and Salesforce loop guards:** don't let an automation trigger itself, and cap how deep chained triggers can go.
- **DST practice:** store the IANA timezone, compute UTC instants in Go, never rely on the database's `CURRENT_DATE`, and test both transition nights.
- **Email law and deliverability:** a pure invoice reminder is *transactional* under CAN-SPAM, so no unsubscribe is legally required, **as long as it contains no promotional content**. Gmail and Yahoo one-click unsubscribe applies to bulk marketing mail. Resend auto-suppresses hard bounces and complaints, but Gmail doesn't report complaints.

## Architecture
```
Cloudflare Cron */15 ──HMAC+ts──> POST /api/internal/automation/tick
   handler: for each servable tenant (bounded concurrency 4, pg_try_advisory_lock per tenant)
      plan():    date rules → candidates → INSERT automation_deliveries
                 ON CONFLICT (occurrence_key) DO NOTHING
      drain():   claim due deliveries (FOR UPDATE SKIP LOCKED, lease 2m)
                 → re-check eligibility (live) → guards → group into digests
                 → send via notify (Idempotency-Key = delivery id) → mark sent
   runs INSIDE the request with a ~50s time budget; leftovers carry to next tick
record write ─> LogAuditFull ─> observers[] ─> automation_outbox (same tenant tx)
                                               drained by the same tick (+ in-proc nudge)
```
**Why the work happens inside the tick request:** Fly's proxy stops idle machines based on traffic. A background goroutine left running after the HTTP response can be killed mid-send. An in-flight request keeps the machine alive, and leases plus idempotency make an interruption safe whatever happens.

### Delivery state machine (`automation_deliveries.status`)
`planned → claimed(lease_until) → sent | skipped(reason) | failed(retry_at) | dead | unknown`

- A notify timeout marks the delivery **unknown**, not failed. It's then reconciled through the existing delivery status lookup (`services/notify_delivery.go:153`) rather than retried blindly, which avoids duplicate emails.
- `skipped` reasons are an enum, shown in the delivery log: `paid`, `voided`, `converted`, `do_not_contact`, `no_email`, `suppressed`, `superseded`, `stale`, `tenant_inactive`, `anchor_changed`, `credit_covers`, `cap`.

### Occurrence key (the core idempotency primitive)
`rule_id : resource : record_id : anchor_date : stage_index`

- **The anchor date is part of the key.** If the due date moves from the 10th to the 20th, the old planned rows fail the re-check (`anchor_changed`) and new ones get planned. This is the Salesforce re-queue behaviour.
- Digest key: `rule_id : recipient_email : local_date`.

### Components
1. **Queue and hook upgrades:**
   - `async_jobs` gets `run_at` and exponential backoff.
   - `AuditObserver` becomes a list of observers and also carries the actor ID, so writes made by the system actor can be ignored for loop protection.
   - New `tenancy.ForEachServable`.
2. **stonesuite-notify (separate repo, small change):** accept an `Idempotency-Key` header and dedupe on (tenant, key) for 7 days. Without this, a timeout followed by a retry sends a duplicate, because notify has no dedupe today.
3. **`automation/` package:**
   - `rule.go`: typed structs.
     - Triggers: `date_offset{dateField, stages[]{offsetDays}, repeatEveryDays, maxRepeats}` and `event{resource, toStatus}`.
     - Plus `conditions`, `stopWhen`, `reenroll bool` and `actions`.
     - Recipients are roles only: `record_contact | owner | approvers | role | user`.
   - Conditions compile through each module's `query.FieldResolver`. That gives the same whitelist and parameterized SQL as list endpoints, so no new SQL surface.
   - `registry.go`: each module registers a `Source` with:
     - `Resource`
     - `DateFields`
     - `Eligible(ctx, q, id, today) (ok, reason)`, the live re-check
     - `LoadVars`
     - `ResolveRecipients`
     - `DigestRender`

     Add a registry test with the expected keys and a `new-module` skill step. The same clone discipline applies as for `approvalchain` and `globalsearch`.
   - `clock.go`: works out tenant-local "today" and quiet hours in Go, and passes `today` to SQL as a `$n` parameter.
   - `guard.go`, `digest.go`, `plan.go`, `drain.go`, `templates/`.
4. **Tenant schema:**
   - `automation_rules`
   - `automation_deliveries`: unique `occurrence_key`, status, reason, `lease_until`, `notify_notification_ids TEXT[]`
   - `automation_outbox`: includes a `depth` column
   - `automation_suppressions`: email, reason, source (manual, bounce, complaint, unsubscribe)
   - `automation_state`: per rule `enabled_at` and `last_planned_at`
   - All created with `IF NOT EXISTS`, with guarded constraints.
5. **Controllers:** under `/api/tenant/automation/*`, with TenantResolver and `Require("automation", …)`:
   - rule CRUD
   - preview/dry-run, which returns who would get what and when
   - delivery log
   - tenant kill switch

   Plus public `GET /api/automation/stop?t=<signed>` ("stop reminders for this invoice" or "unsubscribe"), rate-limited.

## Edge-case catalog → how the design handles each

### Invoices / collections (AR)
| Case (verified in code) | Handling |
|---|---|
| `ODUE` is never set automatically (`invoice/aging.go:104` computes it instead) | Never use status as the trigger. Overdue means `due_date < tenant_today`. |
| PAID can revert to PART or SENT after a payment is unapplied or voided, or a credit memo is reversed (`invoice/balance.go:101`) | The plan step is stateless and re-run every tick, so reminders resume automatically. Earlier stages already marked `skipped:paid` aren't re-sent. Only later stages fire. |
| **A VOID invoice can come back to life** through a payment unapply (an existing bug) | `Eligible` explicitly excludes VOID and PAID **and** requires `balance_due > 0`. The underlying bug is flagged separately. |
| Partial payment | The email shows the **live** balance at send time. Partial payments don't restart the cadence. |
| Customer holds unapplied credit ≥ balance (`payment_unapplied_amount`, `credit_memo_unapplied_amount`) | Don't remind the customer (`skipped:credit_covers`). Send an in-app note to the owner: "apply credit". |
| `due_date` NULL, or edited after sending | NULL means never planned. An edit changes the anchor: old rows are skipped with `anchor_changed` and new ones planned. |
| Invoice marked SENT but never emailed, or emailed while still APPV (status and sending are decoupled) | Requires status in (SENT, PART, ODUE) **and** at least one `document_sends` row for the record. We never chase an invoice the customer never received. |
| Soft-deleted invoice or customer | `Eligible` checks `deleted_at IS NULL` on both. |
| `customer_do_not_contact` (S:1216), which nothing enforces today | Honoured: `skipped:do_not_contact`. The document-send gap is flagged separately. |
| `customer_ar_status` is Collections, Bad Debt or Credit Hold | Automated customer reminders stop. Owner gets an in-app note instead. |
| Recipient email: the invoice keeps its own snapshot `invoice_bill_email`, which can be empty or stale; `customer_accounts_email` is the AP address | Order of preference: `customer_accounts_email` → `invoice_bill_email` → skip with `no_email` and notify the owner in-app. |
| Customer has 5 overdue invoices | One digest email per recipient per local day, listing all 5, with one signed PDF link per invoice. |
| Customer pays between planning and sending | Re-checked at claim time, so it's skipped as `paid`. Rows are planned no earlier than 24h ahead. |
| Refunds don't touch the invoice balance | No special case. We rely on `balance_due`. |

### Quotes / estimates (deals)
| Case | Handling |
|---|---|
| Converting doesn't change the quote's status; it stays SENT forever (`salesorder/store_convert.go:354`) | `Eligible` excludes quotes with a `quote_conversion` row and estimates with a linked quote. |
| EXPR, RJCT and CANC are final; nothing sets EXPR automatically | Excluded. Auto-expiry is a P4 action. |
| `valid_until` is NULL | Never planned. |

### Approvals (internal ops)
| Case | Handling |
|---|---|
| There's no "pending since" column | Read the latest `<module>_history` row where `to_status` is the gated status. |
| **A disabled approver still counts as a required signer, so the record is stuck forever** (`approvalchain/engine.go:427`, an existing bug) | Escalation goes only to active approvers (`users.status='active'`, employee not deleted). If **no** active approver remains, escalate to the owner and the super_admin role with the reason "no active approver". The bug is flagged separately. |
| Approvers reconfigured mid-flight; a gate removed leaves a stale `pending` | Re-checked at send time against the current approver config. If the status is no longer gated, the row is skipped. |
| Rejected then resubmitted | The anchor is the new history row, so it's a new occurrence and the escalation clock starts again. |

### Install / fabrication
| Case | Handling |
|---|---|
| Three sources of truth for the install date (job, sales order, schedule row) | The source of truth is the `sales_order_schedule` Installation activity, because that's what planners edit. The anchor date is part of the key, so reschedules are handled. |
| Job HOLD, CANC, or `job_cancel_requested_at` set; supply-only delivery | Excluded. |

### Vendor bills
| Case | Handling |
|---|---|
| A payment is already scheduled (SCHD) for the bill | Skip "bill due" alerts when a SCHD vendor payment covers the balance. (`RunSchedulerTick` has no callers, a pre-existing gap; flagged.) |

### Time, scheduling and infrastructure
| Case | Handling |
|---|---|
| DB `CURRENT_DATE` is server-UTC; due dates are plain `DATE` | `tenant_today` is computed in Go from the IANA timezone and passed as a parameter. `CURRENT_DATE` is never used in automation SQL. |
| DST nights: spring-forward 2am doesn't exist, fall-back 1am happens twice | Quiet hours are 8am–6pm, so they avoid 2–3am entirely. The digest runs at 8am local. Keys use the local *date*, never a local time, so nothing doubles. Tests cover both nights. |
| Weekends and holidays | Per-rule option to hold external sends until the next weekday. Holidays are out of scope for v1. |
| Ticks missed (Cloudflare outage, deploy) | Catch-up planning from `last_planned_at`. **Superseded**: if stages 1 and 2 are both overdue, send only the latest. **Stale**: a stage more than 48h late is skipped with reason `stale`. No backlog of blasts. |
| A rule is enabled on a tenant that already has 200 overdue invoices | **No retroactive blast.** Only occurrences scheduled after `enabled_at` count. The preview shows the count, and a daily tenant cap throttles anything left over. |
| Overlapping ticks, or a manual retry | `pg_try_advisory_lock(tenant)` plus the unique occurrence key plus leases. |
| Machine stops mid-drain | The lease expires and the row is reclaimed. The notify idempotency key prevents a double send. A delivery in `unknown` gets reconciled. |
| Neon connection pressure (~30 tenants × MaxConns 5) | Bounded concurrency of 4 tenants at a time. Each tenant needs a handful of indexed queries. |
| Tenant suspended, or user disabled, mid-queue | Re-checked at send time: `tenant_inactive`. Internal recipients are filtered to `users.status='active'`. |
| Tick endpoint abused | HMAC over timestamp and body, ±5 min window, per-IP rate limit, `logSecurityEvent`. It can only plan and drain idempotent work, so replaying it does nothing. |

### Messaging, compliance and abuse
| Case | Handling |
|---|---|
| Spam relay through user-defined rules | Recipients are resolved only from registry roles; free-text external addresses aren't allowed. Per-contact caps: 1 email per rule per day and 3 automated emails per contact per week. Plus a per-tenant daily cap. |
| Rule loops (an automation triggering automations) | Writes by the system actor never trigger event rules. Outbox `depth` is capped at 3. Per-record, per-rule cooldown. |
| Hard bounce or complaint | Follows the pull model from the Resend delivery-status spec: no new webhook into the backend. The drain step reads `services.EmailStatuses` for this recipient's earlier automation sends (their `notify_notification_ids`). A `bounced`, `complained` or `suppressed` status adds the address to `automation_suppressions` (source `bounce` or `complaint`), and the current send is skipped with reason `suppressed`. Until that spec ships, Resend's own suppression list is the backstop. Gmail never reports complaints, so we also cap frequency. |
| Transactional vs commercial content | Built-in AR, deals and ops templates contain **no promotional content**, so they're CAN-SPAM transactional. AI-drafted copy is checked by a promo-content guard and needs human approval. All external emails still carry a "stop these reminders" link, which costs nothing and protects deliverability. |
| Prompt injection via customer data into AI copy | AI drafts **templates at authoring time** using placeholder variables, never real customer data at send time. Runtime sends are pure template rendering. |

## Existing bugs found during research (fix separately, before or alongside P1)
1. A VOID invoice can be revived by a payment unapply or credit reversal (`invoice/balance.go:101`; nothing stops voiding an invoice with applied payments).
2. Disabled or deleted approvers still count toward required signers, so records get stuck, and they still receive emails (`approvalchain/engine.go:427`, `notify.go:204`).
3. `customer_do_not_contact` isn't enforced on document send (`controllers/documents.go`).
4. `vendorpayment.RunSchedulerTick` has no callers, so scheduled vendor payments never run (`vendorpayment/scheduler.go:52`). The automation tick is the natural host for it.

## Phasing (each phase gets its own spec → plan → PR)
1. **P0 Foundations:**
   - `run_at` and backoff, observer list with actor, `ForEachServable`, tick endpoint with HMAC, the Cloudflare Worker.
   - Automation tables, RBAC resource, system actor seeding, `clock.go`.
   - Notify `Idempotency-Key` (notify repo).
2. **P1 Built-ins (notify-only):**
   - AR cadence −3/0/+7/+14 with digests.
   - Quote and estimate expiry (−3d to the owner).
   - Approval stuck more than 48h.
   - Install tomorrow.
   - Vendor bill due in 3 days.
   - Daily 8am in-app digest.
   - Delivery log, preview, stop link.
3. **P2 User-defined rules:** CRUD, `query`-based conditions, frontend builder (WebUI repo).
4. **P3 AI:** natural-language rule to validated JSON, AI template drafting with a promo guard, smart suggestions via `globalsearch` AI hooks.
5. **P4 Actions:** auto-EXPR quotes, auto-ODUE invoices, follow-up tasks (needs `crm_activity.due_at`), and hosting `RunSchedulerTick`.

## P0 detail (foundations)

### Control-plane schema (appended, idempotent)
```sql
ALTER TABLE async_jobs ADD COLUMN IF NOT EXISTS run_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS idx_async_jobs_claim ON async_jobs (job_type, status, run_at);
```
- `Claim` adds `AND run_at <= now()`.
- `MarkFailed` sets `run_at = now() + LEAST(2^attempts, 900) seconds`.
- The existing callers (provisioner, importer, docextract) keep working, because `run_at` defaults to now().

### Tenant schema (appended, idempotent; constraints guarded with `DO $$ … pg_constraint`)
| Table | Key columns |
|---|---|
| `automation_rules` | `id UUID PK`, `key TEXT UNIQUE` (built-ins: `ar.invoice_due`, …), `name`, `enabled BOOL`, `system BOOL`, `audience TEXT CHECK (internal\|external)`, `rule JSONB`, `version INT`, `created_by`, `updated_by`, timestamps |
| `automation_state` | `rule_id PK FK`, `enabled_at TIMESTAMPTZ`, `last_planned_at TIMESTAMPTZ` |
| `automation_deliveries` | `id UUID PK`, `rule_id`, `resource`, `record_id`, `anchor_date DATE`, `stage INT`, `occurrence_key TEXT UNIQUE`, `recipient_kind`, `recipient_email`, `recipient_user_id`, `digest_key TEXT`, `run_at TIMESTAMPTZ`, `status TEXT`, `reason TEXT`, `attempts INT`, `lease_until TIMESTAMPTZ`, `notify_notification_ids TEXT[]`, `error TEXT`, timestamps. Indexes: `(status, run_at)`, `(resource, record_id)`, `(digest_key)` |
| `automation_outbox` | `id BIGSERIAL`, `resource`, `record_id`, `action`, `actor_user_id`, `to_status`, `depth INT`, `created_at`, `processed_at` |
| `automation_suppressions` | `email CITEXT PK` (or a lower-cased TEXT), `reason`, `source CHECK (manual\|bounce\|complaint\|unsubscribe)`, `created_at` |
| `automation_settings` (one row) | `kill_switch BOOL`, `daily_external_cap INT DEFAULT 200`, `per_contact_weekly_cap INT DEFAULT 3` |

**System actor: reuse what exists.** The tenant schema already seeds a system employee at `employee_id = 1` (`employee_is_system = TRUE`, "System User", S:~539), used as `created_by` on seed rows.
- Business tables keyed by employee use that row as the automation actor. Its display name is updated to "StoneSuite Automation" with an idempotent `UPDATE … WHERE employee_id = 1 AND employee_first_name = 'System'`.
- `users.id` is needed for `ActorUserID` and audit. For that, seed one `users` row with a fixed UUID, linked through `employee_user_id`, flagged with a new `users.is_system BOOLEAN NOT NULL DEFAULT false` (ADD COLUMN IF NOT EXISTS), and with no identity row, so it can't log in.
- It is excluded wherever `userstore` lists users, counts seats, or fills assignee pickers.
- The implementation plan must first confirm that nothing else treats employee 1 specially in a way this would break.

### Code
- `workflow/audit.go`: `RegisterAuditObserver(fn)` appends to a slice guarded by a mutex or atomic copy-on-write. The signature gains `actorUserID`. The RAG observer is re-registered through the new API, with no change in behaviour.
- `tenancy/foreach.go`: `ForEachServable(ctx, cp, router, concurrency, fn func(ctx, *Tenant, *pgxpool.Pool) error) error`. Errors are collected per tenant; one tenant failing never stops the others.
- `controllers/automation_tick.go`: `POST /api/internal/automation/tick`. It sits outside `/api/tenant`, with no JWT; auth is HMAC-SHA256 over `ts + "." + body`, keyed by `AUTOMATION_TICK_SECRET`, with a ±300s window, compared in constant time. It returns 200 `{tenants, planned, sent, skipped, deferred, durationMs}` after the budget of `AUTOMATION_TICK_BUDGET` (default 50s). On failure it calls `logSecurityEvent(r, "automation_tick_bad_sig")`. Rate-limited per IP.
- `config.AppConfig`: adds `AutomationEnabled`, `AutomationTickSecret`, `AutomationTickBudget`, `AutomationDefaultTZ` (`America/Chicago`).
- `automation/clock.go`: `TenantClock{Loc}`, with `Today() civil-date`, `InQuietHours(t)` and `NextSendWindow(t)`. Table tests cover 2026-03-08 and 2026-11-01 in America/Chicago, New_York, Los_Angeles, Denver and Phoenix (no DST).
- `authz` catalog: resource `automation` with actions read/create/update/delete. super_admin gets it automatically.
- `cloudflare/automation-tick/` (Worker + `wrangler.toml`): cron `*/15 * * * *`. It signs and POSTs to the tick URL, which is set per environment through Worker secrets.
- **stonesuite-notify:** an `Idempotency-Key` request header. `UNIQUE (tenant_id, idempotency_key)` on notifications, kept 7 days. A duplicate returns the original notification ids with 200. `services.NotificationRequest` gains an `IdempotencyKey string` field.

## P1 detail (built-in rules, notify-only)

| Key | Audience | Default | Trigger | Recipients | Stop / eligibility |
|---|---|---|---|---|---|
| `ar.invoice_due` | external | off | `invoice_due_date` stages −3, 0, +7, +14 | customer AP email → bill email | AR eligibility table above; digest per recipient per day |
| `ar.invoice_overdue_owner` | internal | on | `invoice_due_date` +1 | record owner (in-app) | same eligibility |
| `deals.quote_expiring` | internal | on | `quote_valid_until` −3 | owner (in-app + email) | not converted, status SENT/APPV |
| `deals.estimate_expiring` | internal | on | `estimate_valid_until` −3 | owner | no linked quote |
| `ops.approval_stuck` | internal | on | gated-status history `changed_at` +2d, repeat every 2d, max 3 | active approvers, else owner + super_admin | still pending at the same gate |
| `ops.install_tomorrow` | internal | on | schedule Installation activity −1 | owner + install crew | not HOLD/CANC/cancel-requested, not supply-only |
| `ap.vendor_bill_due` | internal | on | `vendor_bill_due_date` −3 | AP role (in-app) | APPV/PART/ODUE, balance > 0, no covering SCHD payment |
| `digest.daily` | internal | on | 08:00 local | each active user | in-app summary of their items above |

**Templates:** embedded in `automation/templates/`, rendered with `services.RenderEmail`. Variables are only what `LoadVars` provides. They contain no promotional content. Every external email carries the signed "stop these reminders" link (scoped to rule + record) and a public PDF link minted with `DocLinkURL` at send time.

**Endpoints (P1):** all under `/api/tenant/automation`, with TenantResolver and `Require("automation", action)`:
- `GET /rules`
- `PATCH /rules/{key}`: enable, disable, stage offsets
- `POST /rules/{key}/preview`: next 14 days of would-be deliveries, plus the count of records skipped and why
- `GET /deliveries?status=&rule=&cursor=`: keyset-paginated
- `PATCH /settings`: kill switch, caps

Public, rate-limited: `GET /api/automation/stop?t=`.

**Help doc:** add `docs/` end-user page "Automated reminders" to the embedded corpus (AI rule 6).

## Next steps after approval
1. Write the spec to `docs/superpowers/specs/2026-10-14-automation-engine-design.md` (P0 + P1 detail plus this edge-case catalog) and commit it. The user reviews it.
2. Run `superpowers:writing-plans` for P0, then execute via `/feature`, followed by the migration-auditor, tenancy-security-reviewer and module-drift-checker agents.

## Verification
- `go build ./... && go test ./...`, plus `go test -tags dbtest ./...` on a fresh database with pgvector.
- **Table-driven:**
  - occurrence-key generation
  - stage superseding and staleness
  - `tenant_today` across timezones and both 2026 DST nights
  - quiet-hours deferral
  - rule validation, and condition compilation (an invalid field gives `InvalidFilterError`)
- **DB tests, with a fake notify server:**
  - Invoice due in 3 days → tick → 1 send. Tick again → still 1.
  - Paid before the send → `skipped:paid`.
  - Due date edited → `anchor_changed`, and the new date is planned.
  - PAID→PART revert → later stages resume.
  - VOID with a balance → never sent.
  - 5 invoices for one customer → 1 digest.
  - Rule enabled with a backlog → 0 retroactive sends.
  - Notify timeout → `unknown` → reconciled, with no duplicate.
  - Disabled approver → escalation excludes them; none active → owner and super_admin.
  - `do_not_contact` → skipped.
- **Security:** bad signature, stale timestamp and replay on the tick endpoint. RBAC denied on rule CRUD. IDOR (cross-tenant rule ID → 404).
- **On dev:** the Cloudflare Worker fires every 15 min, sweeps show up in `fly logs`, the delivery log endpoint works, and notify delivery status matches.

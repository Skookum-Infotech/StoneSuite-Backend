# Automation P1a — AR Dunning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first real automation: a built-in, off-by-default invoice dunning rule that emails customers one digest per day for their overdue/soon-due invoices, built on the P0 foundations.

**Architecture:** `ARDunning` is a `Job` that runs **plan → drain** inside the existing tick. *Plan* turns each eligible invoice's cadence stages into `automation_deliveries` rows (idempotent on `occurrence_key`). *Drain* claims due rows, re-checks each against live data, applies guards, groups them into one digest per recipient per local day, sends via notify with an idempotency key, and records the outcome. The generic drain (claim/lease/guard/send/state machine) is separate from the AR-specific source and email so later jobs (quotes, approvals, bills) reuse it.

**Tech Stack:** Go, pgx, testify (table-driven), `services.SendNotificationWithResult`, tenant `dbtest` tag for DB tests.

**Spec:** `docs/superpowers/specs/2026-10-14-automation-engine-design.md` (AR table, state machine, occurrence key, digest, guards). Prior plan: `docs/superpowers/plans/2026-10-14-automation-engine-p0.md`.

## Scope

In: Job env extension, AR rule seed, stage planning, AR eligibility, planner, generic drain + guards, AR digest email, rule enable/disable API.
**Deferred to P1b** (each its own plan): quote/estimate expiry, stuck approvals, install and vendor-bill reminders, daily in-app digest, delivery-log and preview endpoints, public stop-reminders link, `unknown`-delivery reconciliation, event/outbox rules (with P0 deferrals M3 observer registration and M4 outbox depth). Because the stop link is deferred and the rule ships **disabled**, external sends are transactional invoice reminders only (no promotional content).

## Global Constraints

- "Today" is tenant-local, computed in Go (`JobEnv.Today`), passed as a `$n` param. Automation SQL never uses `CURRENT_DATE`.
- External sends only inside `InSendWindow` (08:00–18:00 tenant-local); otherwise rows stay `planned` with `run_at = NextSendWindow`.
- Every send sets `NotificationRequest.IdempotencyKey` = lowercase hex sha256 (64 bytes); the recipient is never part of the key's meaning beyond the digest it already names.
- Writes run under `automation.WithSystemActor(ctx)`.
- Schema changes are append-only and idempotent (`ADD COLUMN IF NOT EXISTS`, `ON CONFLICT DO NOTHING`); never edit an existing `CREATE TABLE`.
- Lease 2 minutes; batch cap 50 deliveries per tenant per tick; retry backoff 5m, 30m, 2h; `dead` after 5 attempts.
- Skip reasons (enum strings, `automation_deliveries.reason`, max 32 chars): `paid`, `voided`, `do_not_contact`, `no_email`, `suppressed`, `superseded`, `stale`, `anchor_changed`, `credit_covers`, `cap`, `pre_enable`, `kill_switch`, `not_sent`, `ar_hold`.
- Default cadence offsets (days from `invoice_due_date`): `-3, 0, 7, 14`. A stage more than 48h late is `stale`; if several stages are due, only the latest sends and earlier ones are `superseded`.
- Files stay under 300 lines; tests are table-driven testify; DB tests carry `//go:build dbtest`.

## Review Focus

1. Invoice with status SENT/PART/ODUE but **VOID/PAID revived** or `balance_due = 0` → never sent (`voided`/`paid`).
2. **Due date edited** after planning → old pending rows `skipped:anchor_changed`, new anchor planned, no double send.
3. Customer with **5 overdue invoices** → exactly one email listing all 5; with one signed link each.
4. Rule enabled with a **200-invoice backlog** → zero retroactive sends (`pre_enable`).
5. **Notify timeout** → delivery `unknown`, never auto-retried (no duplicate email); crash mid-send → lease expiry reclaims with the same notify key.

---

### Task 1: Extend `Job.Run` with a tenant environment

**Files:**
- Modify: `automation/runner.go`, `automation/jobs.go`, `automation/runner_test.go`, `controllers/automation_test.go` (any fake `Job`), `main.go` (DefaultJobs call)

**Interfaces:**
- Produces:
  ```go
  type JobEnv struct { Tenant *tenancy.Tenant; Now, Today time.Time; Loc *time.Location }
  type Job interface { Name() string; Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) }
  type Sender interface { Send(ctx context.Context, req services.NotificationRequest) (services.NotificationResult, error) }
  type Deps struct { Send Sender; LinkURL func(tenantID, recordID string) string }
  func DefaultJobs(d Deps) []Job
  ```
  `Result` gains `Sent int, Failed int`; `Summary` and the tick JSON gain `sent`/`failed_sends` counters.

- [ ] **Step 1: Failing test** in `runner_test.go`: a fake job asserts `env.Tenant.Slug`, `env.Loc` (tenant timezone from company profile, fallback otherwise) and `env.Today == TenantToday(env.Now, ...)`; assert `Summary.Sent` aggregates a fake job's `Result.Sent`.
- [ ] **Step 2: Run** `go test ./automation -run TestRunner -v` → FAIL (compile).
- [ ] **Step 3: Implement** the types above. `Runner.runTenant` builds `JobEnv` using the existing `TenantLocation`; `VendorPaymentDispatch.Run` ignores everything but `env.Today`. `DefaultJobs(d)` returns `VendorPaymentDispatch{}` for now. In `main.go` pass `Deps{Send: services notify adapter, LinkURL: controllers.DocLinkURL}` (adapter is a 3-line struct in `automation/send.go` calling `services.SendNotificationWithResult`).
- [ ] **Step 4: Run** `go build ./... && go test ./automation ./controllers` → PASS.
- [ ] **Step 5: Commit** `refactor(automation): pass a JobEnv to jobs and inject send/link deps`.

---

### Task 2: Schema — `sent_at` column, weekly-cap index, seeded AR rule

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append after the automation block, EOF)
- Test: `automation/schema_dbtest_test.go`

**Interfaces:**
- Produces: column `automation_deliveries.sent_at TIMESTAMPTZ`; index `idx_automation_deliveries_contact_sent ON automation_deliveries (recipient_email, sent_at) WHERE status = 'sent'`; one `automation_rules` row `rule_key='ar_dunning'`, `name='Invoice payment reminders'`, `audience='external'`, `is_system=TRUE`, `enabled=FALSE`, `rule='{"cadence":[{"offsetDays":-3},{"offsetDays":0},{"offsetDays":7},{"offsetDays":14}]}'`; matching `automation_state` row (`enabled_at` NULL). Also a Go const `RuleKeyARDunning = "ar_dunning"` and struct `ARRuleConfig{Cadence []struct{OffsetDays int `json:"offsetDays"`}}`.

- [ ] **Step 1: Failing dbtest**: after applying the schema twice, `ar_dunning` exists exactly once, `enabled=false`, cadence parses to `[-3,0,7,14]`; `sent_at` and the index exist.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run TestSchema -v` → FAIL.
- [ ] **Step 3: Implement** the appended SQL: `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`, `INSERT ... ON CONFLICT (rule_key) DO NOTHING`, and the state row via `INSERT ... SELECT id FROM automation_rules WHERE rule_key='ar_dunning' ON CONFLICT DO NOTHING`.
- [ ] **Step 4: Run** the dbtest twice (idempotency) → PASS. Run `migration-auditor` agent on the diff.
- [ ] **Step 5: Commit** `feat(automation): sent_at, weekly-cap index and seeded ar_dunning rule`.

---

### Task 3: Pure stage planning and key builders

**Files:**
- Create: `automation/stages.go`, `automation/keys.go`
- Test: `automation/stages_test.go`, `automation/keys_test.go`

**Interfaces:**
- Produces:
  ```go
  type StageAction string // "plan", "superseded", "stale", "pre_enable"
  type StagePlan struct { Stage int; Date time.Time; Action StageAction }
  // anchor and today are tenant-local dates (time.Time at midnight UTC for the date);
  // enabledOn is the tenant-local date the rule was enabled.
  func PlanStages(anchor time.Time, offsets []int, today, enabledOn time.Time) []StagePlan
  func OccurrenceKey(ruleID, resource, recordID string, anchor time.Time, stage int) string // "rule:resource:record:YYYY-MM-DD:stage"
  func DigestKey(ruleID, recipientEmail string, localDate time.Time) string                // "rule:email(lowercased):YYYY-MM-DD"
  func NotifyKey(occurrenceKeys []string) string                                          // sha256hex of sorted keys joined by "|"
  ```
- Rules for `PlanStages`: a stage is considered only when its date ≤ `today`+1 day (planned at most 24h ahead). Stages dated before `enabledOn` → `pre_enable`. Among remaining stages with date ≤ today, the **latest** is `plan` if ≤ 2 days old else `stale`; earlier ones are `superseded`. Future stages within the 24h look-ahead are `plan`.

- [ ] **Step 1: Failing tests** (table-driven): cadence `[-3,0,7,14]`, due `2026-11-10`; today `2026-11-08` → stage 0 `plan`; today `2026-11-18` with enabledOn `2026-11-01` → stages 0..2 `superseded`, stage 2 (date 11-17) `plan`; today `2026-11-25` → stage 3 (11-24) `plan`, 0..2 `superseded`; today `2026-12-10` → latest `stale`; enabledOn `2026-11-20` → all earlier stages `pre_enable`. Key tests: `OccurrenceKey` format, `DigestKey` lowercases email, `NotifyKey` is order-independent, always 64 chars.
- [ ] **Step 2: Run** `go test ./automation -run 'TestPlanStages|TestKeys' -v` → FAIL.
- [ ] **Step 3: Implement** both files per the rules above; dates compare as `YYYY-MM-DD`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(automation): pure dunning stage planning and key builders`.

---

### Task 4: AR source — candidates and live eligibility

**Files:**
- Create: `automation/ar_source.go`
- Test: `automation/ar_source_dbtest_test.go`

**Interfaces:**
- Produces:
  ```go
  type ARInvoice struct { UUID, Number, CustomerName, Email string; OwnerEmployeeID *int; Anchor time.Time; Balance string }
  func listARCandidates(ctx context.Context, q Querier, today time.Time) ([]ARInvoice, error)
  // live re-check at send time; reason "" means eligible. Returns the live balance and due date.
  func arEligible(ctx context.Context, q Querier, invoiceUUID string, plannedAnchor, today time.Time) (ok bool, reason string, live ARInvoice, err error)
  ```
  (`Querier` = the small `Query/QueryRow` interface already used in `companyprofile`; define locally if none shared.)
- Candidate SQL: status code in (`SENT`,`PART`,`ODUE`), `invoice_balance_due > 0`, `invoice_due_date IS NOT NULL`, invoice and customer not soft-deleted, **and** at least one `document_sends` row for `invoice_uuid` (`not_sent` otherwise). Recipient email = first non-empty of `customer_accounts_email`, `invoice_bill_email`. `listARCandidates` selects `due_date <= today + 1` only after the cadence's earliest negative offset window: use `invoice_due_date BETWEEN today-60 AND today+3` (bounds the scan; older invoices are `stale` anyway).
- `arEligible` reasons, first match wins: deleted/VOID → `voided`; status PAID or `balance_due <= 0` → `paid`; `customer_do_not_contact` → `do_not_contact`; customer AR status code in (`CRHD`,`COLL`,`BDBT`) → `ar_hold`; no document_sends → `not_sent`; due date ≠ `plannedAnchor` → `anchor_changed`; empty email → `no_email`; customer unapplied credit (payments unapplied + credit memos unapplied, reuse the existing SQL expressions behind `payment_unapplied_amount`/`credit_memo_unapplied_amount`) ≥ balance → `credit_covers`.

- [ ] **Step 1: Failing dbtests** — one case per reason above plus: eligible invoice returns live balance after a partial payment; PAID→PART revert is eligible again; **VOID with balance > 0** → `voided`; NULL due date is not a candidate; email preference order (`customer_accounts_email` over `invoice_bill_email`).
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run TestAR -v` → FAIL.
- [ ] **Step 3: Implement** with parameterized SQL only; confirm exact column names (`invoice_deleted_at`, status code join through `lkp_record_status`) by reading the invoice DDL and `invoice/aging.go` before writing.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(automation): AR dunning candidate query and live eligibility`.

---

### Task 5: AR planner

**Files:**
- Create: `automation/ar_plan.go`
- Test: `automation/ar_plan_dbtest_test.go`

**Interfaces:**
- Consumes: `PlanStages`, `OccurrenceKey`, `DigestKey`, `listARCandidates`, `arEligible`, `ARRuleConfig`.
- Produces: `func (j *ARDunning) plan(ctx context.Context, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned, skipped int, err error)` where `ruleRow{ID string; Config ARRuleConfig; EnabledOn time.Time}` and `loadRule(ctx, pool, key) (ruleRow, enabled bool, err error)`.
- Behavior: load the rule; if disabled, return immediately (no queries beyond the rule row). For each candidate, for each `PlanStages` result: `plan` → `INSERT ... ON CONFLICT (occurrence_key) DO NOTHING` with `recipient_kind='external'`, `digest_key`, `run_at = NextSendWindow(env.Now, env.Loc)` (or the stage date's window start if later); `superseded`/`stale`/`pre_enable` → insert as `status='skipped'` with that reason (same conflict rule, so they are written once). Any existing `planned`/`failed` rows for the same record whose `anchor_date` differs from the current due date → `status='skipped', reason='anchor_changed'`. Candidates with empty email: insert one `skipped:no_email` row per occurrence (no owner notification in P1a — recorded in the deferred list). `automation_state.last_planned_at` updated at the end.

- [ ] **Step 1: Failing dbtests**: plan twice → row count unchanged; due-date move 10th→20th → old rows `anchor_changed`, new rows planned; rule disabled → zero rows; **backlog of 200 overdue invoices with `enabled_at=today`** → only `skipped:pre_enable` rows, zero `planned`; five invoices for one customer share one `digest_key`.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement**; wrap writes in `WithSystemActor`; batch inserts in one transaction per tenant.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(automation): AR dunning planner with idempotent occurrence rows`.

---

### Task 6: Generic drain — claim, guards, send, state machine

**Files:**
- Create: `automation/drain.go`, `automation/guards.go`
- Test: `automation/drain_dbtest_test.go`, `automation/guards_test.go`

**Interfaces:**
- Consumes: `Sender`, `JobEnv`, `NotifyKey`.
- Produces:
  ```go
  type Delivery struct { ID, RuleID, Resource, RecordID, OccurrenceKey, DigestKey, RecipientEmail string; Anchor time.Time; Attempts int }
  // Group is one outbound email: all claimed deliveries sharing a digest_key.
  type Group struct { DigestKey, RecipientEmail string; Items []Delivery }
  // Renderer builds the email and live line items for a group; items it drops are
  // returned with a skip reason. It must not send.
  type Renderer interface {
      Render(ctx context.Context, env JobEnv, g Group) (req services.NotificationRequest, kept []Delivery, dropped map[string]string, err error)
  }
  func drain(ctx context.Context, pool *pgxpool.Pool, env JobEnv, send Sender, r Renderer, ruleID string) (Result, error)
  func guardExternal(ctx context.Context, q Querier, email string, now time.Time) (reason string, err error)
  ```
- Behavior: `UPDATE ... SET status='claimed', lease_until=now+2m WHERE id IN (SELECT id ... WHERE rule_id=$1 AND ((status IN ('planned','failed') AND run_at<=now) OR (status='claimed' AND lease_until<now)) AND attempts<5 ORDER BY run_at LIMIT 50 FOR UPDATE SKIP LOCKED)`. Outside `InSendWindow` nothing is claimed. Per group: `guardExternal` order — tenant `kill_switch` → `kill_switch`; email in `automation_suppressions` → `suppressed`; per-contact weekly cap (`per_contact_weekly_cap`, counted on `sent_at` in the last 7 local days, one digest = one count) → `cap`; tenant daily external cap → `cap`. Guarded groups: all items `skipped` with that reason. Otherwise `Render`; dropped items `skipped` with the renderer's reason; if none remain, done. Send with `IdempotencyKey = NotifyKey(kept occurrence keys)`. Success → `sent`, `sent_at=now`, store notify ids. Error that is a timeout/context-deadline (`errors.Is(err, context.DeadlineExceeded)` or `net.Error.Timeout()`) → `unknown` (no retry). Other error → `failed`, `attempts+1`, `run_at = now + backoff[attempts]`, `last_error` truncated to 500 chars; at 5 attempts → `dead`. Every status UPDATE guards with `WHERE id=$1 AND status='claimed'`.

- [ ] **Step 1: Failing tests** with a fake `Sender` and fake `Renderer`: success path marks `sent` with `sent_at`; two runs → one send (idempotent); suppressed email → `skipped:suppressed`; weekly cap boundary (3 sent in 7 days → 4th `cap`); kill switch; send outside window claims nothing; timeout error → `unknown` and a second drain does not resend; transient error → `failed` with later `run_at`, 5th failure → `dead`; expired lease is reclaimed and sends with the **same** `IdempotencyKey`; guard unit tests table-driven.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'TestDrain|TestGuard' -v` → FAIL.
- [ ] **Step 3: Implement** both files; one pool connection per group, no long transactions held across the network call.
- [ ] **Step 4: Run** → PASS; also `go test -race ./automation`.
- [ ] **Step 5: Commit** `feat(automation): generic delivery drain with leases, guards and retry states`.

---

### Task 7: AR digest email and `ARDunning` job wiring

**Files:**
- Create: `automation/ar_email.go`, `automation/ar_job.go`
- Modify: `automation/jobs.go` (`DefaultJobs` adds `&ARDunning{...}`)
- Test: `automation/ar_email_test.go`, `automation/ar_job_dbtest_test.go`

**Interfaces:**
- Produces: `type ARDunning struct{ deps Deps }`; `func (*ARDunning) Name() string` → `"ar_dunning"`; `Run(ctx, pool, env)` = `loadRule` → return if disabled → `plan` → `drain` with `arRenderer`. `arRenderer.Render` re-runs `arEligible` per item using live data (balance shown is the **live** balance), builds `services.Email` content and `NotificationRequest{EventType:"automation.ar_reminder", Resource:"invoice", ResourceID: first invoice UUID, Channels:["email"], Title, Body, Email: ...}` with `TenantID: env.Tenant.ID`, no `ActorUserID`, recipient `RecipientTarget{Email: g.RecipientEmail}` (mirror how `controllers/documents.go` builds external recipients), and one `deps.LinkURL(env.Tenant.ID, invoiceUUID)` per invoice.
- Email content rules: plain transactional wording ("Payment reminder", list of invoice number, due date, days overdue/until due, live balance, signed PDF link), company name from `companyprofile.Get`, **no promotional text**. Pure helper `renderARDigest(company string, items []ARInvoice, today time.Time) services.Email` is unit-tested; `deps.LinkURL` returning `""` omits the link and still sends.

- [ ] **Step 1: Failing tests**: `renderARDigest` table (single invoice upcoming, overdue, five invoices ordering by due date asc, empty link); end-to-end dbtest with fake `Sender`: invoice due in 3 days, rule enabled → tick → exactly 1 send; tick again → still 1; mark invoice PAID before send → `skipped:paid`, 0 sends; five invoices for one customer → 1 send listing 5; customer `do_not_contact` → 0 sends; rule disabled → 0 rows.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'TestAR' -v` → FAIL.
- [ ] **Step 3: Implement** both files and the `DefaultJobs` change.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test -race ./...` and the dbtest set → PASS. Run `tenancy-security-reviewer` on the diff.
- [ ] **Step 5: Commit** `feat(automation): AR dunning job emails one digest per customer per day`.

---

### Task 8: Rule enable/disable API, docs, help

**Files:**
- Create: `controllers/automation_rules.go`, `controllers/automation_rules_test.go`
- Modify: `main.go` (routes), `authz/catalog.go` only if `ResourceAutomation` lacks `read`/`configure` actions (check first), `CLAUDE.md`, `docs/*` generated by `go run ./cmd/gen-apidocs`
- Modify (if one covers invoices/reminders): the matching file in the embedded help corpus under `docs/`

**Interfaces:**
- Produces routes (auth + TenantResolver, tenant-scoped): `GET /api/tenant/automation/rules` → `{success, rules:[{ruleKey,name,audience,enabled,enabledAt,cadence}]}` (`Require(automation, read)`); `PUT /api/tenant/automation/rules/{key}` body `{"enabled": bool}` (`Require(automation, configure)`), 404 on unknown key, 400 on bad body. Enabling sets `automation_state.enabled_at = now` (only when previously disabled; re-enabling resets it, so a rule re-enabled after a pause never blasts a backlog); disabling leaves rows untouched. Response is the updated rule. Log `automation_rule_changed` via `slog` with the tenant, rule key, actor identity and new state; permission denials use `logSecurityEvent`.

- [ ] **Step 1: Failing tests** (fake store + fake enforcer, table-driven): unauthenticated 401; no permission 403; unknown key 404; invalid body 400; enable sets `enabledAt`; enable twice keeps the first `enabledAt`; disable then enable resets it.
- [ ] **Step 2: Run** `go test ./controllers -run TestAutomationRules -v` → FAIL.
- [ ] **Step 3: Implement** handlers and routes; run `go run ./cmd/gen-apidocs`; add a short "AR dunning" paragraph to the CLAUDE.md Automation Engine section (rule off by default, `ar_dunning` key, drain order, deferred items) and a help-doc note if a relevant doc exists.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test -race ./...` including `TestGeneratedAPIDocsAreCurrent` → PASS.
- [ ] **Step 5: Commit** `feat(automation): enable/disable API for automation rules`.

---

## Rulings made while planning (change any before execution)

1. **Digest notify key** hashes the sorted member occurrence keys, not just the digest key. A digest key alone would make notify drop a second, different email to the same customer on the same day as a duplicate. Cost: if a crash leaves a partly sent digest and new invoices join before the retry, the key differs and the customer may get one overlapping email. Accepted for P1a.
2. **No owner in-app note** for `no_email` / `credit_covers` in P1a; those only appear as skipped rows (the delivery log is P1b). Mapping an invoice's `employee_id` owner to a notify user needs its own design.
3. **Stop-reminders link is deferred** to P1b; rule ships disabled, content is strictly transactional.
4. **Planning window** is `due_date ∈ [today-60, today+3]`, so very old invoices are never newly chased when a rule is first enabled.
5. **Cadence is stored in the rule JSON** (editable later in P2) but there is no edit API in P1a.

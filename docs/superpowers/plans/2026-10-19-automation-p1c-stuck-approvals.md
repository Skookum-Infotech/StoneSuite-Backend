# Automation P1c — Stuck Approval Reminders Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remind the approvers who still owe a sign-off when a record has sat at an approval gate for 2, 4 and 6 days, escalating to the record owner and super admins when no approver can act.

**Architecture:** One new rule, `approval_stuck` (internal audience, seeded disabled), one new job, `approval_reminders`. The job reuses the P1a/P1b engine: plan writes `automation_deliveries` rows (one per record × recipient × stage), the generic `drain` claims, guards, renders and sends. Gates come from the `approvalchain` registry, so every notify-wired module is covered without per-module code. Recipients are internal, so only the tenant kill switch applies (the P1b-2 internal path).

**Tech Stack:** Go, pgx, testify (table-driven), `dbtest` build tag with a scratch Postgres.

**Spec:** `docs/superpowers/specs/2026-10-14-automation-engine-design.md` (row `ops.approval_stuck`, Approvals edge-case table). Builds on merged P1a/P1b-1/P1b-2 (`automation/expiry_*.go` is the closest template).

## Global Constraints

- "Today" is tenant-local from `JobEnv.Today`/`JobEnv.Loc`; automation SQL never uses `CURRENT_DATE`; dates are `$n` params.
- Writes run under `WithSystemActor(ctx)`; automation SQL runs only through `Querier`/the tenant pool.
- Rule seeded **disabled**, `is_system`, `audience='internal'`, cadence `[{"offsetDays":2},{"offsetDays":4},{"offsetDays":6}]` (days after the record entered the gate; max 3 reminders).
- Every send carries the engine's idempotency key (`NotifyKey` over occurrence keys); nothing here builds its own.
- Recipients are live users only: `approvalchain.LiveApproverJoin` (employee not deleted, `users.status='active'`).
- Files stay under 300 lines; no `any`; constants for repeated strings; errors wrapped with `%w`.
- DB tests use the `dbtest` tag; schema statements are idempotent (`ON CONFLICT DO NOTHING`); run `go run ./cmd/gen-apidocs` only if routes change (none expected).
- Skip reasons are free text in `automation_deliveries.reason` (no CHECK): new ones are `not_pending`, `recipient_changed`.
- Plan-level rulings (record in the ledger as `Ruling:` lines):
  1. **Module scope:** only registry modules with `DisplayName != ""` (notify-wired, have a frontend route). Quote/estimate/sales order are not covered (no `OwnerColumn`/route entry); follow-up.
  2. **Gate scope:** gates with `Reject != RejectInPlace`. In-place gates (payment, refund, credit memo, vendor credit) sit on the record's first status where the creator may still be editing, so "pending since" is meaningless and reminders would be noise.
  3. **Entry time:** `COALESCE(MAX(history.at WHERE to_status_id = gate), created_at)`; history `at` is a `TIMESTAMP` written in UTC, so it is read as UTC and converted to the tenant location before taking its calendar date.
  4. **Resubmission:** a reject-and-resubmit writes a new history row, giving a new anchor date and so fresh occurrence keys (clock restarts); the old rows die as `anchor_changed`.

## Review Focus

1. A record sent for approval late in the local evening (UTC already tomorrow) must anchor to the tenant-local date. → Task 2 test.
2. Two approvers, one already signed: only the unsigned one is reminded; once everyone has signed the record has left the gate and the planned row dies `not_pending`. → Tasks 2 and 4 tests.
3. Every configured approver deactivated (orphaned gate): owner plus active super admins get the reminder, deduped by user, and an inactive owner is dropped. → Task 2 test.
4. A rejected-in-place or ungated record (zero configured approvers) must never be reminded. → Task 2 test.
5. A record bounced back to draft and resubmitted restarts the clock instead of inheriting the old stage. → Task 3 test.

---

### Task 1: Rule seed, spec derivation, route helper

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append seed), `automation/rule.go`, `approvalchain/route.go`, `controllers/automation_rules_dbtest_test.go`
- Create: `automation/approval_spec.go`, `automation/approval_spec_test.go`
- Test: `approvalchain/route_test.go` (extend)

**Interfaces:**
- Produces: `const RuleKeyApprovalStuck = "approval_stuck"`; `approvalchain.RecordRoute(resource, recordID string) string` (exported wrapper of `resourceRoute`); `type approvalSpec struct { Workflow, DisplayName, RecordTypeCode string; Gate approvalchain.Gate; Cfg approvalchain.ModuleConfig }`; `func approvalSpecs() []approvalSpec` (one per eligible module × gate, ordered by workflow key then gate order); `func (s approvalSpec) ident() error` (validates every interpolated registry identifier against `^[a-z][a-z0-9_]*$`).
- Consumes: `approvalchain.Keys()`, `ForWorkflowKey`, `ModuleConfig.Gates`, `Gate.Reject`.

- [ ] **Step 1: Write failing tests**
  - `TestApprovalSpecs`: every spec has `DisplayName != ""`; none has `Gate.Reject == RejectInPlace`; `installation` yields two specs (gates `TMPL`, `QCPD`); `purchase_order`, `vendor_bill`, `invoice`, `expense`, `bin_transfer` each appear once; `quote`, `estimate`, `sales_order`, `payment`, `refund`, `credit_memo`, `vendor_credit` never appear; every spec's `ident()` is nil.
  - `TestRecordRoute`: `RecordRoute("invoice","abc") == "/sales/invoice/abc"`, unknown resource `""`.
  - Extend `TestListIncludesExpiryRules` (rename `TestListIncludesBuiltInRules`) to also assert `approval_stuck`: disabled, audience `internal`, cadence `[{2},{4},{6}]`.
- [ ] **Step 2: Run to verify failure** — `go test ./automation/ ./approvalchain/ -run 'ApprovalSpecs|RecordRoute'`; expect undefined symbols.
- [ ] **Step 3: Implement.** Seed `approval_stuck` plus its `automation_state` row exactly like the `quote_expiry` seed (find it at the end of `schema.sql`; same columns, `ON CONFLICT DO NOTHING`). `approvalSpecs` iterates `approvalchain.Keys()`, skips configs with empty `DisplayName`, emits one spec per gate whose `Reject != RejectInPlace`.
- [ ] **Step 4: Verify** — same command plus `go build ./...`; dbtest `TestListIncludesBuiltInRules` against a fresh scratch DB.
- [ ] **Step 5: Commit** — `feat(automation): approval_stuck rule seed and gate specs`.

### Task 2: Candidates, recipients, live eligibility

**Files:**
- Create: `automation/approval_source.go`, `automation/approval_source_dbtest_test.go`

**Interfaces:**
- Consumes: `approvalSpec` (Task 1), `Querier`, `approvalchain.LiveApproverJoin`, `approvalchain.StatusRejected`, `localDate(t, loc)`, `sameDay`.
- Produces:
  - `type ApprovalRecord struct { UUID, Number, OwnerEmail string; OwnerEmployeeID int; InternalID int; Entered time.Time /* tenant-local date at midnight */ }`
  - `type approvalRecipient struct { UserID, IdentityID, Email, Name string; Fallback bool }`
  - `func listApprovalCandidates(ctx, q Querier, spec approvalSpec, today time.Time, loc *time.Location) ([]ApprovalRecord, error)`
  - `func approvalRecipients(ctx, q Querier, spec approvalSpec, rec ApprovalRecord) ([]approvalRecipient, error)`
  - `func approvalState(ctx, q Querier, spec approvalSpec, uuid string, plannedAnchor time.Time, recipientUserID string, loc *time.Location) (reason string, rec ApprovalRecord, rcpt approvalRecipient, err error)` — `reason ""` = still eligible; precedence `voided`, `not_pending`, `anchor_changed`, `recipient_changed`.
  - consts `reasonNotPending = "not_pending"`, `reasonRecipientChanged = "recipient_changed"`.
- Candidate rules (SQL interpolates only `ident()`-validated registry names, values are `$n`): record not soft-deleted; status code = gate code within the record type (join `lkp_record_status`/`lkp_record_type` like `expiry_source.go`); approval-status column ≠ `rejected`; at least one configured active approver at (type, gate status) (`approvalchain.ApproverCounts`'s `configured > 0`); entry date within `[today-8, today-2]` (constants `approvalMinAgeDays = 2`, `approvalMaxAgeDays = 8`, the largest seeded offset 6 plus `maxStageAgeDays`).
- Recipient rules: live approvers configured at the gate who have **no** row in the approval table for this record at this status; if live approvers = 0 (orphaned): the record owner (live user only) plus every active user holding the `super_admin` role (`roles.key`, `user_roles`), deduped by user id, `Fallback=true`.

- [ ] **Step 1: Write failing dbtests** (reuse the scratch-tenant helpers used by `expiry_source_dbtest_test.go`; build records on the `purchase_order` module). Cases, each its own subtest:
  - eligible record 3 days at gate → listed, `Entered` = local date; a record 1 day old and one 9 days old → not listed.
  - **local-date anchor (Review Focus 1):** history `at` = `2026-03-09 02:30` UTC with loc `America/Chicago` → `Entered` = 2026-03-08.
  - **two approvers, one signed (Review Focus 2):** only the unsigned approver returned.
  - **orphaned (Review Focus 3):** all approvers' users disabled → owner + two super admins, owner who is also super admin returned once, disabled owner omitted, all `Fallback`.
  - **rejected / ungated (Review Focus 4):** approval status `rejected` → not listed; zero configured approvers → not listed.
  - `approvalState`: record advanced out of the gate → `not_pending`; history re-entered later → `anchor_changed`; approver since signed → `recipient_changed`; soft-deleted → `voided`.
- [ ] **Step 2: Run to verify failure** — `go test -tags dbtest ./automation/ -run Approval` with `TEST_DATABASE_URL` pointing at a fresh scratch DB (see CLAUDE.md recipe); expect undefined symbols.
- [ ] **Step 3: Implement** the three functions. Entry timestamp is scanned as `time.Time`, interpreted with `time.UTC` wall-clock then converted via `localDate(.., loc)`. One query per candidate for recipients is acceptable (stuck records are few); note it in a comment.
- [ ] **Step 4: Verify** — dbtests green; `go vet ./automation/`; file < 300 lines (split `approval_recipients.go` if not).
- [ ] **Step 5: Commit** — `feat(automation): approval gate candidates, recipients and live check`.

### Task 3: Planner

**Files:**
- Create: `automation/approval_plan.go`, `automation/approval_plan_dbtest_test.go`

**Interfaces:**
- Consumes: Task 2 functions, `PlanStages`, `OccurrenceKey`, `DigestKey`, `insertDeliveries`, `supersedeAnchors`, `markPlanned`, `NextSendWindow`, `deliveryInsert`, `recipientInternal`.
- Produces: `func planApprovals(ctx, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned, skipped int, err error)` and `func approvalInsertsFor(spec approvalSpec, recs []approvalRecipients, env JobEnv, rule ruleRow) []deliveryInsert` (pure).
- Row shape: one row per (record, recipient, stage). `Key = OccurrenceKey(rule.ID, spec.Workflow, uuid+"~"+gateCode+"~"+recipientUserID, entered, stage)` (`~` separates; recipient part is `none` when a record has no recipient, which writes a single skipped `no_email` row). `record_id` column holds the plain record uuid. `Digest = DigestKey(rule.ID, email, sendDay)` so an approver gets **one digest per day across all modules**. All in one transaction under `WithSystemActor`; `supersedeAnchors` per spec.

- [ ] **Step 1: Write failing tests**
  - pure `TestApprovalInsertsFor` (table): stage selection matches `PlanStages` (record 3 days old → stage 0 plan; 5 days → stage 1 plan, stage 0 superseded; 9 days → stale; enabled today → `pre_enable`), two recipients → two rows with distinct keys sharing the digest key per email, orphaned/no recipient → one `no_email` skipped row.
  - dbtest idempotency: planning twice inserts rows once.
  - **restart on resubmit (Review Focus 5):** record leaves and re-enters the gate with a later history row → new keys planned, old open rows skipped `anchor_changed`.
- [ ] **Step 2: Run to verify failure**, **Step 3: Implement**, **Step 4: Verify** (unit + dbtest), 
- [ ] **Step 5: Commit** — `feat(automation): approval reminder planner`.

### Task 4: Digest renderer

**Files:**
- Create: `automation/approval_email.go`, `automation/approval_email_test.go`, `automation/approval_email_dbtest_test.go`

**Interfaces:**
- Consumes: `approvalState`, `approvalchain.RecordRoute`, `services.AppURL`, `companyprofile.Get`, `services.Email`/`Detail`, `Renderer`, `Group`, `Delivery`.
- Produces: `type approvalRenderer struct { pool *pgxpool.Pool }` implementing `Renderer`; `func renderApprovalDigest(company string, items []approvalLine, today time.Time) services.Email` (pure); `type approvalLine struct { DisplayName, Number, Route string; DaysWaiting int; Fallback bool }`; consts `approvalEventType = "automation.approval_stuck"`.
- Behaviour: for each item call `approvalState` with the delivery's recipient; ineligible items are dropped with that reason; survivors become one email + in-app notification addressed to the group's recipient (`RecipientTarget{UserID: identityID, Email}`), `Channels: {arChannelMail, expiryChannelApp}`, `Link` and email `ActionURL` = in-app route of the **oldest** item (never a public no-login link), details rows "`<DisplayName> <Number>` — waiting N days". If any kept item is a fallback recipient, add one paragraph saying no active approver is configured and why they received it. Title/body count the items. Content is transactional with no promotional text.

- [ ] **Step 1: Write failing tests**: pure render (singular/plural, oldest-first ordering, fallback paragraph present only for fallback, link is `/purchases/vendor_bill/<id>` style app route); dbtest `Render` drops `not_pending` and `recipient_changed` rows and keeps live ones.
- [ ] **Step 2–4:** fail, implement, verify.
- [ ] **Step 5: Commit** — `feat(automation): approval reminder digest email`.

### Task 5: Job, wiring, docs

**Files:**
- Create: `automation/approval_job.go`, `automation/approval_job_dbtest_test.go`
- Modify: `automation/jobs.go`, `CLAUDE.md`

**Interfaces:**
- Produces: `type ApprovalReminders struct{ deps Deps }` with `Name() == "approval_reminders"` and `Run(ctx, pool, env) (Result, error)`: `loadRule(RuleKeyApprovalStuck)` → return empty `Result` if disabled → `planApprovals` → `drain(..., approvalRenderer{pool}, rule.ID)`, adding the planner's skipped count to `Result.Skipped` (copy `ExpiryReminders.runSpec`). Registered in `DefaultJobs`.

- [ ] **Step 1: Write failing dbtests**: enabled rule + record stuck 3 days + fake sender → exactly one send to the unsigned approver, a second `Run` sends nothing; disabled rule → no rows written; kill switch on → row deferred (`planned`, `run_at` pushed), nothing sent; an approver who signs between plan and drain → row `skipped/recipient_changed`/`not_pending`, nothing sent.
- [ ] **Step 2–4:** fail, implement, verify (`go test ./...`, dbtests for `automation` and `controllers`, `golangci-lint run ./...`).
- [ ] **Step 5: Docs + commit.** CLAUDE.md Automation item 7: approval_stuck rule, cadence, gate/module scope rulings, internal path, recipient fallback. Commit `feat(automation): stuck approval reminders job` then `docs: automation stuck approval reminders`.

---

## Self-review

- **Spec coverage:** trigger (+2d repeat, max 3) → Task 1 seed + Task 3; active approvers only → Task 2; no active approver → owner + super_admin → Task 2; still pending at same gate → `approvalState`; resubmission restarts clock → Task 3; internal default (spec says "on", repo ruling is seeded disabled like all rules so tenants opt in) → recorded.
- **Deliberately out of scope:** quote/estimate/sales order approvals, in-place gates, a per-approver "who hasn't signed" list in the app, preview support for non-AR rules, WebUI changes.
- **Known cost of rulings:** in-place gates and the three sales documents get no reminders until a follow-up; a record that bounces and resubmits within one local day keeps its keys (same anchor date).

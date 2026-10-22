# Automation P1b-2: Quote and Estimate Expiry Reminders — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Email each quote/estimate owner one daily digest of their sent quotes and estimates that expire in 3 days (or expired up to 2 days ago), via the existing automation tick.

**Architecture:** A second `Job` (`ExpiryReminders`) with two system rules (`quote_expiry`, `estimate_expiry`), reusing P1a's `PlanStages`, `OccurrenceKey`, `DigestKey` and the generic `drain`. The drain gains an *internal-recipient* path (kill switch only; no suppression list, weekly contact cap or external daily cap). Owners resolve through `employee.employee_user_id → users` exactly as `approvalchain.ownerContact` does.

**Tech Stack:** Go, pgx v5, testify, `dbtest` tag.

**Spec:** `docs/superpowers/specs/2026-10-14-automation-engine-design.md` (§ "Quotes / estimates (deals)": converting leaves the quote SENT so exclude converted; EXPR/RJCT/CANC final; `valid_until` NULL never planned). Base: branch `feat/automation-p1b1-hardening`.

## Global Constraints

- "Today" is tenant-local from `JobEnv`; no `CURRENT_DATE` in automation SQL. Writes run under `WithSystemActor`.
- Every send: `IdempotencyKey = NotifyKey(occurrence keys)`; transactional wording only; no promotional text.
- Rules ship **disabled** (`enabled=FALSE`, `is_system=TRUE`, `audience='internal'`), cadence `{"cadence":[{"offsetDays":-3}]}` stored in the rule JSON (`ARRuleConfig` shape reused). Schema is append-only/idempotent (`ON CONFLICT DO NOTHING`).
- Stage semantics from P1a: no look-ahead, latest due stage is `plan` if ≤ 2 days old else `stale`, stages dated on or before the enable day are `pre_enable`.
- Owner must resolve to an **active** user (`users.status='active'`, `employee_deleted_at IS NULL`, `employee_user_id` set) with a non-empty email; otherwise the occurrence is recorded `skipped` with reason `no_email` (reused enum value).
- Files < 300 lines; `go run ./cmd/gen-apidocs` only if routes change (none here); update CLAUDE.md.
- DB tests: `TEST_DATABASE_URL="postgres://prasannakumarnagaboyina@/ss_p1a_t7?host=/tmp"` with `-tags dbtest`, re-runnable (delete deliveries before rules, restore seeded rules).

## Review Focus

1. A quote that was converted to a sales order (still status SENT, has a `quote_conversion` row) must never be reminded (Task 2).
2. Owner has 6 expiring documents → one email listing all (quotes and estimates together per day is NOT required; per-rule digest is fine) (Task 4).
3. `valid_until` moved after planning → old row `anchor_changed`, new date planned (Task 3).
4. Owner disabled/deleted between planning and send → skipped, not sent to a stale address (Task 4).
5. Internal sends must not consume the external daily cap or the customer weekly contact cap (Task 1).

---

### Task 1: Internal-recipient path in the drain

**Files:**
- Modify: `automation/drain.go` (`Delivery`, `drainGroup`), `automation/drain_state.go` (`claimDue` returns `recipient_kind`), `automation/guards.go`
- Test: `automation/drain_internal_dbtest_test.go` (new), `automation/guards_test.go`

**Interfaces:**
- Produces: `Delivery.RecipientKind string` (`"external"`/`"internal"`); `func guardInternal(s automationSettings) string` — returns `reasonKillSwitch` when `s.KillSwitch`, else `""`.
- Behaviour: in `drainGroup`, rows with `RecipientKind == "internal"` run only `guardInternal` (deferral semantics from P1b-1 apply to `kill_switch`); they skip `guardExternal`'s suppression/weekly checks and `guardDailyCap`. A group never mixes kinds (digest keys include the recipient).

- [ ] **Step 1: Failing tests** — `TestGuardInternal` table (kill switch on → `kill_switch`, off → ""); dbtest `TestDrainInternalIgnoresCapsAndSuppression`: weekly cap 1, daily cap 1, recipient suppressed, an internal row still sends; same setup with an external row defers/skips as before; `TestDrainInternalKillSwitchDefers`.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'TestGuardInternal|TestDrainInternal' -v` → FAIL.
- [ ] **Step 3: Implement** the field, the `claimDue` projection, `guardInternal`, and the branch in `drainGroup`.
- [ ] **Step 4: Run** the same command and `go test -race ./automation` → PASS (all P1a/P1b-1 drain tests unchanged).
- [ ] **Step 5: Commit** `feat(automation): internal-recipient path in the drain`.

### Task 2: Schema seed and expiry candidate source

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append), `automation/rule.go` (`RuleKeyQuoteExpiry = "quote_expiry"`, `RuleKeyEstimateExpiry = "estimate_expiry"`)
- Create: `automation/expiry_source.go`, `automation/expiry_source_dbtest_test.go`
- Test: extend `automation/schema_dbtest_test.go` (seeded rules exist, disabled, internal)

**Interfaces:**
- Produces: `type ExpiryDoc struct{ UUID, Number, CustomerName, OwnerEmail, OwnerUserID, OwnerIdentityID string; ValidUntil time.Time }`; `type expirySpec struct{ RuleKey, Resource, Table string }` with two package vars `quoteSpec` / `estimateSpec`; `func listExpiryCandidates(ctx context.Context, q Querier, spec expirySpec, today time.Time) ([]ExpiryDoc, error)` — documents with status code `SENT` for the right record type, `valid_until IS NOT NULL`, `valid_until BETWEEN today-2 AND today+3`, not soft-deleted; quotes excluded when a `quote_conversion` row exists; estimates excluded when any non-deleted quote has `quote_estimate_id` = the estimate; owner joined through `employee`/`users` (columns empty when it does not resolve); `func expiryEligible(ctx context.Context, q Querier, spec expirySpec, uuid string, plannedValidUntil time.Time) (ok bool, reason string, live ExpiryDoc, err error)` — same filters live, reasons: `voided` (deleted), `converted`, `paid` is NOT used; use `not_sent` for any status other than SENT, `anchor_changed` when `valid_until` differs from the planned date, `no_email` when the owner does not resolve to an active user with an email.
- Seeds: two `automation_rules` rows (`quote_expiry` "Quote expiry reminders", `estimate_expiry` "Estimate expiry reminders", `enabled=FALSE`, `is_system=TRUE`, `audience='internal'`, cadence `-3`) plus `automation_state` rows, mirroring the `ar_dunning` seed.
- Look up status/record-type ids through `lkp_record_status` joined to `lkp_record_type` by code (`QUOT`/`ESTM` — confirm the estimate record-type code in the schema); never hard-code numeric ids.

- [ ] **Step 1: Failing tests** — dbtest fixtures (follow `ar_source_dbtest_test.go`): sent quote expiring in 3 days listed with resolved owner; draft (non-SENT) quote absent; converted quote absent (Review Focus 1); quote with NULL `valid_until` absent; estimate with a linked quote absent; soft-deleted absent; owner with no linked user listed with empty `OwnerEmail`; `expiryEligible` returns `anchor_changed` after `valid_until` moves and `no_email` after the owner's user is disabled; schema test asserts both seeded rules are disabled, `audience='internal'`.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'TestExpiry|TestAutomationSchema' -v` → FAIL.
- [ ] **Step 3: Implement** the schema append, constants, and `expiry_source.go` (one parameterized SQL builder per spec; table/column names come from the two package-level specs, never from input).
- [ ] **Step 4: Run** the same command twice (re-runnable) and `go test -race ./automation` → PASS.
- [ ] **Step 5: Commit** `feat(automation): expiry reminder rules and candidate queries`.

### Task 3: Expiry planner

**Files:**
- Create: `automation/expiry_plan.go`, `automation/expiry_plan_dbtest_test.go`

**Interfaces:**
- Consumes: Task 2's `listExpiryCandidates`/`ExpiryDoc`; P1a's `loadRule`, `PlanStages`, `OccurrenceKey`, `DigestKey`, `NextSendWindow`, `localDate`, `insertDeliverySQL`.
- Produces: `func planExpiry(ctx context.Context, pool *pgxpool.Pool, env JobEnv, spec expirySpec, rule ruleRow) (planned, skipped int, err error)` — transactional, mirrors `ARDunning.plan`: one row per `PlanStages` result, `recipient_kind='internal'`, `recipient_email` = owner email, `recipient_user_id` = owner user id (uuid, nullable), digest key `DigestKey(rule.ID, ownerEmail, localDate(runAt))`, rows with no resolvable owner/email recorded `skipped`/`no_email`, `ON CONFLICT (occurrence_key) DO NOTHING`, and the P1a "anchor changed" supersede UPDATE keyed by `(rule, resource, record_id)`; updates `automation_state.last_planned_at`.

- [ ] **Step 1: Failing tests** — dbtest: stage 3 days out is NOT planned (no look-ahead, stage dated today); document whose stage date is today is planned once and a second `planExpiry` inserts 0; `valid_until` moved → old planned row `skipped:anchor_changed`, new date planned (Review Focus 3); stage dated before the enable day → `pre_enable`; owner without email → `no_email`; 6 documents, one owner → all share one `digest_key`.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run TestPlanExpiry -v` → FAIL.
- [ ] **Step 3: Implement** `planExpiry`; if the batch-insert code duplicates `planTx` more than ~25 lines, extract the shared insert-and-count helper from `ar_plan.go` (its tests must stay green).
- [ ] **Step 4: Run** the command twice and `go test -race ./automation` → PASS.
- [ ] **Step 5: Commit** `feat(automation): expiry reminder planner`.

### Task 4: Digest email, `ExpiryReminders` job, registration

**Files:**
- Create: `automation/expiry_email.go`, `automation/expiry_job.go`, tests `automation/expiry_email_test.go`, `automation/expiry_job_dbtest_test.go`
- Modify: `automation/jobs.go` (`DefaultJobs` adds `&ExpiryReminders{deps: d}`)

**Interfaces:**
- Produces: `type ExpiryReminders struct{ deps Deps }` with `Name() string` → `"expiry_reminders"` and `Run(ctx, pool, env) (Result, error)` that, for each of `quoteSpec`, `estimateSpec`: `loadRule` → skip if disabled → `planExpiry` → `drain(ctx, pool, env, deps.Send, expiryRenderer{...}, rule.ID)`; results summed, errors joined (one spec failing must not stop the other).
- `func renderExpiryDigest(company, noun string, items []ExpiryDoc, today time.Time) services.Email` (pure; `noun` = "quote"/"estimate"; lists number, customer, expiry date, "expires in N days"/"expired N days ago", owner-facing wording, link to the record via `deps.LinkURL(env.Tenant.ID, uuid)` when non-empty).
- `expiryRenderer.Render` re-runs `expiryEligible` per item (drops with its reason), builds `NotificationRequest{EventType:"automation.expiry_reminder", Resource: spec.Resource, Channels: ["email","in_app"], Recipients: [{UserID: owner identity id, Email: owner email}]}`, no `ActorUserID`. Internal owners are real users, so `in_app` is enabled.

- [ ] **Step 1: Failing tests** — pure table `TestRenderExpiryDigest` (one upcoming, one expired, six items ordered by expiry then number, empty link omitted); dbtest with fake `Sender`: enabled rule + quote expiring today → exactly 1 send, second tick still 1; owner disabled before send → `skipped:no_email`, 0 sends (Review Focus 4); converted-before-send → skipped, 0 sends; six docs one owner → 1 send; a tenant with a saturated external daily cap still sends internal digests (Review Focus 5); disabled rule → 0 rows; `DefaultJobs` contains `expiry_reminders`.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'TestRenderExpiry|TestExpiryJob' -v` → FAIL.
- [ ] **Step 3: Implement** the three pieces and register in `DefaultJobs`.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test -race ./...` and the dbtests twice → PASS. Run the `tenancy-security-reviewer` agent on the diff.
- [ ] **Step 5: Commit** `feat(automation): quote and estimate expiry reminders`.

### Task 5: Docs and API exposure check

**Files:** Modify `CLAUDE.md` (Automation Engine section: expiry rules, internal drain path), `controllers/automation_rules_dbtest_test.go` (assert `GET`-store `List` returns the new seeded rules with their cadence).

- [ ] **Step 1:** Add the CLAUDE.md paragraph (rule keys, disabled by default, no look-ahead, internal path ignores external caps).
- [ ] **Step 2:** Add the list assertion; run `go test -tags dbtest ./controllers -run 'TestSetEnabled|TestList' -v` and `go test ./...` → PASS.
- [ ] **Step 3: Commit** `docs: automation expiry reminders`.

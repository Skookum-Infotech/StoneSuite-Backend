# Automation P1b-1: Engine Hardening, Delivery Log and Preview — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the P1a known gaps (caps and kill switch defer instead of dropping, disabling a rule cancels its unsent rows, timed-out sends count toward caps) and give admins a delivery log and a dry-run preview.

**Architecture:** All changes sit on the P1a base (`feat/automation-p1a-ar-dunning @ 13806df`). Drain guards stop calling `markSkipped` for `cap`/`kill_switch` and instead push `run_at` forward (the existing 48h `sweepStale` bounds the deferral). The two new endpoints are read-only tenant routes in `controllers/`, backed by SQL in `automation/` (preview reuses the planner's row-building loop without inserting).

**Tech Stack:** Go, pgx v5, testify (table-driven), `dbtest` build tag for DB tests.

**Spec:** `docs/superpowers/specs/2026-10-14-automation-engine-design.md` (rows "Delivery state machine", "Caps", "preview/dry-run, delivery log"). P1a plan: `docs/superpowers/plans/2026-10-16-automation-p1a-ar-dunning.md`.

**Not in this plan (separate plans, each needs its own decision):** quote/estimate expiry, stuck approvals, install and vendor-bill reminders (new Jobs); the public stop-reminders link (unauthenticated, signed-token endpoint); `unknown` reconciliation (needs the notify repo's idempotency-replay behaviour verified first — until then `unknown` rows stay unknown and now count toward caps, Task 2).

## Global Constraints

- Schema changes are append-only and idempotent (none are expected here; `reason` is free text `VARCHAR(32)`).
- No `CURRENT_DATE` in automation SQL; "today" and day boundaries come from `JobEnv` (`Today`, `Loc`).
- Every tenant route: `tenantChain(...)`, `Require(automation, read)` for GETs/preview, errors via `fail(...)`, denials via `logSecurityEvent`.
- Files stay under 300 lines; `go run ./cmd/gen-apidocs` after adding routes (`TestGeneratedAPIDocsAreCurrent`).
- Skip/defer reasons are an enum in `automation/guards.go`; new value this plan: `rule_disabled`.
- DB tests: scratch DB `TEST_DATABASE_URL="postgres://prasannakumarnagaboyina@/ss_p1a_t7?host=/tmp"` with `-tags dbtest`; every dbtest must be re-runnable (clean up rows it creates, deliveries before rules).

## Review Focus

1. A tenant at its daily cap with 300 due digests: nothing is lost, the overflow goes out the next day(s) in order, and nothing older than 48h sends (Task 1).
2. Kill switch on for a day then off: rows planned before it send after, not a blast of stale ones (Task 1).
3. A timed-out (`unknown`) send followed by a new digest to the same contact counts against the weekly cap (Task 2).
4. Admin disables the rule because the copy is wrong, re-enables within 48h: the old planned rows do not go out (Task 3).
5. Delivery log pagination with identical `created_at` values neither skips nor repeats rows; a hostile `status`/`limit` value is a 400, never SQL (Task 4).

---

### Task 1: Defer capped and kill-switched groups

**Files:**
- Modify: `automation/drain.go` (`drainGroup`), `automation/drain_state.go`, `automation/guards.go`
- Test: `automation/drain_guard_dbtest_test.go` (extend), `automation/guards_test.go`

**Interfaces:**
- Produces: `func deferRows(ctx context.Context, q execer, items []Delivery, runAt, now time.Time) error` in `drain_state.go` — sets still-`claimed` rows to `planned`, `run_at = runAt`, `lease_until = NULL`, attempts unchanged.
- Produces: `func isDeferral(reason string) bool` in `guards.go` — true for `reasonCap` and `reasonKillSwitch`.
- Consumes: `NextSendWindow(now, loc)` (`window.go`), `localDayStart`.

- [ ] **Step 1: Failing tests** — `TestDrainDefersWhenDailyCapReached`: cap 1, two digests due; one email sends, the other row is `planned` with `run_at` = next send window after the current local day (not `skipped`); a second drain on the next local day sends it. `TestDrainDefersUnderKillSwitch`: switch on → zero sends, row `planned`, `run_at` pushed ≥ next window; switch off and `Now` +1 day → sends. `TestDrainStaleAfterLongDeferral`: row deferred past 48h of its original `run_at` is skipped `stale`, never sent. Table test `TestIsDeferral` (cap, kill_switch → true; suppressed, stale, "" → false).
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'TestDrain(Defers|Stale)|TestIsDeferral' -v` → FAIL.
- [ ] **Step 3: Implement** `deferRows` and `isDeferral`; in `drainGroup`, when `reason != ""` and `isDeferral(reason)`, call `deferRows` with `runAt = NextSendWindow(localDayStart(env.Today, env.Loc).AddDate(0,0,1), env.Loc)` for `cap`, and `NextSendWindow(env.Now, env.Loc)` plus 1 hour for `kill_switch`; log `automation_event=drain_deferred` with reason and group size. The per-contact weekly cap is also a `cap` deferral (same next-day runAt). `suppressed` still goes through `markSkipped`. Deferred rows do not add to `Result.Skipped`.
- [ ] **Step 4: Run** the same command → PASS; then `go test -race ./automation`.
- [ ] **Step 5: Commit** `fix(automation): defer capped and kill-switched deliveries instead of dropping them`.

### Task 2: Unknown sends count toward caps

**Files:**
- Modify: `automation/guards.go` (`guardExternal`, `guardDailyCap`)
- Test: `automation/drain_guard_dbtest_test.go`

**Interfaces:**
- Consumes: `automation_deliveries.status IN ('sent','unknown')`; `unknown` rows have `sent_at` NULL, so use `COALESCE(sent_at, updated_at)`.

- [ ] **Step 1: Failing tests** — `TestWeeklyCapCountsUnknown`: weekly cap 1, one prior digest `unknown` 1 day ago for the contact → next digest is deferred (Task 1 behaviour), not sent. `TestDailyCapCountsUnknown`: daily cap 1 and one `unknown` external digest today → next group deferred.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run 'Test(Weekly|Daily)CapCountsUnknown' -v` → FAIL.
- [ ] **Step 3: Implement** by changing both count queries to `status IN ('sent','unknown')` and `COALESCE(sent_at, updated_at) >= $n`. Update the doc comments.
- [ ] **Step 4: Run** → PASS, then `go test -race ./automation`.
- [ ] **Step 5: Commit** `fix(automation): count unknown sends toward contact and daily caps`.

### Task 3: Disabling a rule cancels its unsent rows

**Files:**
- Modify: `controllers/automation_rules_store.go` (`SetEnabled`), `automation/guards.go` (add `ReasonRuleDisabled = "rule_disabled"` exported const)
- Test: `controllers/automation_rules_dbtest_test.go` (new, `dbtest` tag)

**Interfaces:**
- Consumes: `pgAutomationRuleStore.SetEnabled(ctx, key, enabled)` (one transaction already).
- Produces: on `enabled == false` and `prev == true`, in the same transaction: `UPDATE automation_deliveries SET status='skipped', reason='rule_disabled', lease_until=NULL, updated_at=NOW() WHERE rule_id=$1 AND status IN ('planned','failed')`. `claimed` rows are left (in flight).

- [ ] **Step 1: Failing tests** (real DB; also covers the previously untested store): `TestSetEnabledDisableSkipsUnsentRows` (planned and failed rows → skipped/`rule_disabled`; sent and unknown rows untouched), `TestSetEnabledEnableStampsEnabledAtOnce` (enable twice keeps first `enabled_at`; disable→enable resets it), `TestSetEnabledUnknownKey` → `ErrAutomationRuleNotFound`.
- [ ] **Step 2: Run** `go test -tags dbtest ./controllers -run TestSetEnabled -v` → FAIL.
- [ ] **Step 3: Implement** the extra `UPDATE` inside `SetEnabled`; export the reason constant from `automation` and use it.
- [ ] **Step 4: Run** → PASS; `go build ./... && go vet ./...`.
- [ ] **Step 5: Commit** `fix(automation): disabling a rule skips its unsent deliveries`.

### Task 4: Delivery log endpoint

**Files:**
- Create: `controllers/automation_deliveries.go`, `controllers/automation_deliveries_store.go`, `controllers/automation_deliveries_test.go`
- Modify: `main.go` (route), `docs/*` via `go run ./cmd/gen-apidocs`

**Interfaces:**
- Produces: `GET /api/tenant/automation/deliveries?status=&ruleKey=&limit=&cursor=` → `{success, deliveries:[{id, ruleKey, resource, recordId, stage, anchorDate, recipientEmail, status, reason, attempts, runAt, sentAt, lastError, createdAt}], nextCursor}`; `Require(automation, read)`.
- `status` must be one of the seven delivery statuses; `ruleKey` must match an existing rule key charset `[a-z0-9_.-]{1,64}`; `limit` default 25, max 100 (`query.MaxLimit` semantics, clamp above 100, 400 below 1); `cursor` is opaque base64 of `created_at|id`, keyset ordered `created_at DESC, id DESC`. Bad filter/cursor → 400 via the standard `fail`.
- Consumer-side interface `automationDeliveryStore { List(ctx, DeliveryFilter) ([]AutomationDelivery, string, error) }`; Postgres implementation joins `automation_rules` on `rule_id` for `ruleKey`. All values are `$n` parameters.

- [ ] **Step 1: Failing tests** (fake store + fake enforcer, table-driven): 401 unauthenticated; 403 without `read`; 400 for `status=bogus`, `limit=0`, `limit=abc`, malformed `cursor`, `ruleKey=x'--`; 200 default limit 25; `limit=500` clamps to 100; `nextCursor` returned only when more rows exist. DB test (`dbtest`): 3 rows with identical `created_at` paginated with `limit=2` return all 3 exactly once.
- [ ] **Step 2: Run** `go test ./controllers -run TestAutomationDeliveries -v` → FAIL.
- [ ] **Step 3: Implement** handler, store and route (`tenantChain(automationDeliveryOps.List)`); copy the auth/envelope pattern from `controllers/automation_rules.go`; run `go run ./cmd/gen-apidocs`.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test -race ./...` and the dbtest → PASS (including `TestGeneratedAPIDocsAreCurrent`).
- [ ] **Step 5: Commit** `feat(automation): delivery log endpoint`.

### Task 5: Rule preview (dry run)

**Files:**
- Modify: `automation/ar_plan.go` (extract), `automation/ar_job.go`
- Create: `automation/ar_preview.go`, `controllers/automation_preview.go`, tests `automation/ar_preview_dbtest_test.go`, `controllers/automation_preview_test.go`
- Modify: `main.go`, `docs/*` (gen-apidocs)

**Interfaces:**
- Produces in `automation`: `type PlanRow struct{ InvoiceUUID, Number, Email string; Stage int; Anchor, RunAt time.Time; Status, Reason string }`; `func buildARPlanRows(ctx context.Context, q Querier, env JobEnv, rule ruleRow) ([]PlanRow, error)` containing the candidate/stage loop now inline in `planTx` (behaviour unchanged; `planTx` inserts the returned rows with `insertDeliverySQL` exactly as before); `func PreviewAR(ctx context.Context, pool *pgxpool.Pool, env JobEnv, ruleKey string) (Preview, error)` where `Preview{ Counts map[string]int; Recipients []PreviewRecipient }`, `PreviewRecipient{ Email string; Items []PlanRow }`, `Counts` keyed by status/reason (`planned`, `pre_enable`, `stale`, `superseded`, `no_email`). Recipients only include `planned` rows, sorted by email, capped at 200 recipients (`Counts` still total). Previews ignore whether the rule is enabled but treat `enabled_at` NULL as "enabling today" (use `env.Today` as the enable date, matching Task-P1a ruling "stages dated on or before the enable day are `pre_enable`"). It writes nothing.
- Produces route: `POST /api/tenant/automation/rules/{key}/preview` → `{success, counts, recipients}`; `Require(automation, read)`; unknown key → 404 (only `ar_dunning` supported in P1b-1; any other existing key → 400 "preview not supported"). The handler builds `JobEnv` the same way `Runner` does (`newJobEnv`), using the request tenant.

- [ ] **Step 1: Failing tests** — dbtest `TestPreviewARWritesNothing` (counts rows in `automation_deliveries` and `automation_state.last_planned_at` before/after), `TestPreviewARMatchesPlan` (the preview's planned set equals what a real `plan` then inserts for the same env, for an enabled rule), `TestPreviewARNoEmailAndPreEnableCounts`. Controller table tests: 401, 403, 404 unknown key, 400 unsupported key, 200 shape.
- [ ] **Step 2: Run** `go test -tags dbtest ./automation -run TestPreviewAR -v` → FAIL.
- [ ] **Step 3: Implement** the extraction, `PreviewAR`, handler and route; `go run ./cmd/gen-apidocs`. The existing `plan` dbtests must pass unchanged — they prove the extraction is behaviour-preserving.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test -race ./...` and `go test -tags dbtest -race -count=1 ./automation ./controllers` twice → PASS.
- [ ] **Step 5: Commit** `feat(automation): dry-run preview for AR dunning`.

### Task 6: Docs

**Files:** Modify `CLAUDE.md` (Automation Engine section: deferral semantics, `rule_disabled`, caps count `unknown`, the two new endpoints).

- [ ] **Step 1:** Edit the section; keep it to a short paragraph.
- [ ] **Step 2: Run** `go test ./docs ./...` → PASS.
- [ ] **Step 3: Commit** `docs: automation P1b-1 behaviour notes`.

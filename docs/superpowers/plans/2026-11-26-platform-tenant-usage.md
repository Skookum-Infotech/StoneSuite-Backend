# Platform Tenant Usage — implementation plan

Spec: `docs/superpowers/specs/2026-11-26-platform-tenant-usage-design.md` (D1–D3 approved).
Frontend contract: Skookum-Infotech/StoneSuite-WebUI#454, `src/types/platformUsage.ts`.

All steps are **sequential** (each builds on the previous step's types). Every new exported
func gets a one-line doc comment; errors wrapped `fmt.Errorf("context: %w", err)`; no magic
numbers (named consts); `context.Context` first; control-plane queries always filter by
`tenant_id`; tenant data is read only through that tenant's own pool (never across DBs).

Shared vocabulary (defined in step 2, used everywhere after):
- **Usage day** = a UTC calendar day. The rollup for day D runs after D ends.
- **Period window** for `7d|30d|90d` = the N complete UTC days ending at the latest rolled-up day;
  "prev" = the N days before that.
- **Metric keys** (`usage/fleet_metrics.go` consts): `compute_request_hours`,
  `compute_job_hours`, `requests`, `errors_5xx`, `p95_ms`, `db_bytes`, `r2_bytes`,
  `r2_objects`, `active_users`, `emails_sent`, `automation_runs`, `failed_jobs`, `dead_jobs`,
  and `records_created.<resource>` (prefix const `records_created.`). AI tokens are **not** copied:
  they are read from the existing `tenant_llm_usage_daily`.

---

## Step 1 — Schema (control plane)
**Files:** `database/migrations/control_plane/schema.sql` only. Follow the `add-migration` skill
(idempotent, append-only, runs in one transaction on every boot).

Append at the end of the file, each with a header comment in the file's style:
- `tenant_request_hourly (tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  hour TIMESTAMPTZ NOT NULL, requests BIGINT NOT NULL DEFAULT 0, errors_5xx BIGINT NOT NULL
  DEFAULT 0, handler_ms BIGINT NOT NULL DEFAULT 0, p95_ms INT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (tenant_id, hour))`.
- `tenant_usage_daily (tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  usage_date DATE NOT NULL, metric_key TEXT NOT NULL, value NUMERIC NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (tenant_id, usage_date, metric_key))`
  + `CREATE INDEX IF NOT EXISTS idx_tenant_usage_daily_date ON tenant_usage_daily(usage_date)`.
- `tenant_table_sizes (tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  table_name TEXT NOT NULL, total_bytes BIGINT NOT NULL DEFAULT 0, row_estimate BIGINT NOT NULL
  DEFAULT 0, measured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (tenant_id, table_name))`.
- `tenant_usage_alerts (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id UUID NOT NULL
  REFERENCES tenants(id) ON DELETE CASCADE, rule_key TEXT NOT NULL, metric_key TEXT NOT NULL,
  severity TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'open', observed NUMERIC, baseline NUMERIC,
  message TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', first_seen_at TIMESTAMPTZ
  NOT NULL DEFAULT NOW(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), acted_by UUID,
  acted_at TIMESTAMPTZ, snoozed_until TIMESTAMPTZ, resolved_at TIMESTAMPTZ)` with named CHECKs
  `tenant_usage_alerts_severity_chk` (critical|warning|info) and `tenant_usage_alerts_status_chk`
  (open|acked|snoozed|resolved) **inside** the CREATE TABLE (new table, so that is idempotent);
  a partial unique index `idx_tenant_usage_alerts_live ON (tenant_id, rule_key, metric_key)
  WHERE status <> 'resolved'` (one live alert per rule+metric); and
  `idx_tenant_usage_alerts_status ON (status)`.

Verify: `go build ./... && go test ./database/...`.

## Step 2 — Usage store (control-plane reads/writes)
**Files (new):** `usage/fleet_metrics.go`, `usage/fleet_store.go`, `usage/fleet_read.go`,
`usage/alerts_store.go`, `usage/fleet_store_dbtest_test.go`, `usage/fleet_metrics_test.go`.
**Reference:** `usage/store.go` (pool-backed store, `//go:build dbtest` test style in
`usage/store_dbtest_test.go`).

`fleet_metrics.go`: the metric-key consts above; `type HourlyRow struct{TenantID string; Hour
time.Time; Requests, Errors5xx, HandlerMs int64; P95Ms int}`; `type DailyValue struct{TenantID
string; Day time.Time; Key string; Value float64}`; `type TableSize struct{TableName string;
TotalBytes, RowEstimate int64}`; `type Alert struct{ID, TenantID, TenantName, RuleKey, MetricKey,
Severity, Status, Message, Detail string; Observed, Baseline *float64; FirstSeenAt, LastSeenAt
time.Time; SnoozedUntil *time.Time}`; `type AlertFiring struct{TenantID, RuleKey, MetricKey,
Severity, Message, Detail string; Observed float64; Baseline *float64}`; consts for severities
and statuses; pure helper `RecordsCreatedKey(resource string) string` (+ table test).

`fleet_store.go` — `type FleetStore struct{pool *pgxpool.Pool}`, `NewFleetStore(pool)`:
- `UpsertRequestHourly(ctx, rows []HourlyRow) error` — one batch; ON CONFLICT adds
  requests/errors/handler_ms and takes `GREATEST(p95_ms, EXCLUDED.p95_ms)`.
- `UpsertDailyMetrics(ctx, tenantID string, day time.Time, values map[string]float64) error` —
  ON CONFLICT replaces value (rollup is idempotent).
- `ReplaceTableSizes(ctx, tenantID string, measuredAt time.Time, sizes []TableSize) error` — one tx:
  delete tenant's rows, insert new.
- `PruneRetention(ctx, now time.Time) error` — hourly rows older than 90 days, daily rows older
  than 13 months, resolved alerts older than 90 days.

`fleet_read.go` (all filter by tenant_id):
- `HourlyTotals(ctx, tenantID string, from, to time.Time) (HourlyTotals, error)` →
  `{Requests, Errors5xx, HandlerMs int64; P95Ms int}` (p95 = max hourly p95, documented as approx).
- `JobStats(ctx, tenantID string, from, to time.Time) (JobStats, error)` from `async_jobs` rows
  with `updated_at` in [from,to): `JobMs` = sum of `updated_at-created_at` for
  succeeded|failed|dead, `Failed`, `Dead` counts.
- `SignInStats(ctx, tenantID string, from, to time.Time) (SignInStats, error)` →
  `{ActiveUsers int (distinct identities with auth_sessions.last_seen_at in range),
  TotalUsers int (identities of tenant), LastSignInAt *time.Time (max auth_sessions.created_at)}`.
- `DailyValues(ctx, tenantIDs []string, from, to time.Time) ([]DailyValue, error)` (inclusive days).
- `TokensByDay(ctx, tenantIDs []string, from, to time.Time) ([]DailyValue, error)` — sum of
  prompt+completion from `tenant_llm_usage_daily`, Key = `ai_tokens`.
- `TokensBySource(ctx, tenantID string, from, to time.Time) (map[string]int64, error)`.
- `TableSizes(ctx, tenantID string) ([]TableSize, *time.Time, error)` largest first.
- `LatestRolledUpDay(ctx) (*time.Time, error)` — max usage_date in tenant_usage_daily.

`alerts_store.go`:
- `RecordFiring(ctx, f AlertFiring, now time.Time) error` — if a live (non-resolved) alert exists
  for (tenant, rule, metric): update severity/observed/baseline/message/detail/last_seen_at, and
  flip `snoozed` → `open` when `snoozed_until <= now`; else insert `open`.
- `ResolveQuiet(ctx, tenantID string, quietSince time.Time, now time.Time) (int64, error)` — live
  alerts with `last_seen_at < quietSince` → `resolved`, `resolved_at = now`.
- `Watchlist(ctx, now) ([]Alert, error)` — open, or snoozed with `snoozed_until <= now`; joins
  `tenants.display_name`; ordered severity (critical, warning, info) then last_seen_at desc.
- `TenantAlerts(ctx, tenantID string, since time.Time) ([]Alert, error)` — any status, first_seen desc.
- `TopSeverityByTenant(ctx, now) (map[string]string, error)` — from the same predicate as Watchlist.
- `AckAlert(ctx, alertID, actorIdentityID string, snoozeDays int, now time.Time) (bool, error)` —
  0 → `acked`; 7|30 → `snoozed` with `snoozed_until = now + days`; returns false when no live alert has that id.

Tests: dbtest-tagged round trips for every method (skip cleanly without `TEST_DATABASE_URL`), incl.
p95 GREATEST merge, snooze expiry flip, one-live-alert uniqueness, retention pruning.

## Step 3 — Request meter + attribution
**Files:** new `usage/request_meter.go`, `usage/request_meter_test.go`; edit
`middleware/logging.go`, `middleware/auth.go`, `middleware/middleware_test.go` (or a new
`middleware/attribution_test.go`), `main.go`, `shutdown.go` only if needed.
**Reference:** `usage/recorder.go` (async writer with Start/Close, never blocks the request).

- `middleware`: add `RequestObserver interface{ Observe(tenantID string, status int, elapsed time.Duration) }`
  and `NewRequestLogger(obs RequestObserver) func(http.Handler) http.Handler`; keep
  `RequestLogger` as a thin wrapper `NewRequestLogger(nil)` so existing callers/tests compile.
  The logger puts a per-request `*attribution{tenantID, identityID string}` slot in the context;
  `RequireAuth` fills it after a successful token check (unexported helper `setAttribution(ctx, payload)`).
  The log line's `tenant_id`/`identity_id` now come from the slot (fallback: current behaviour).
  After the handler, call `obs.Observe(slot.tenantID, status, elapsed)` when obs != nil and tenantID != "".
- `usage.RequestMeter`: `NewRequestMeter(writer hourlyWriter, flushEvery time.Duration)`,
  `Start()`, `Observe(...)` (O(1) under a mutex; buckets keyed by tenant + UTC hour; keeps up to a
  bounded reservoir of latencies per bucket — const, e.g. 512 — for p95), `Close(ctx) error`
  (stops the ticker goroutine and does a final flush). Flush swaps the map out under the lock and
  writes outside it; a failed write is logged via slog and dropped (metering never fails a request).
  `DefaultMeterFlushInterval = 60 * time.Second`. Pure `p95(samples []int) int` with table tests.
- `main.go`: when `cpPool != nil`, build `usage.NewRequestMeter(usage.NewFleetStore(cpPool), usage.DefaultMeterFlushInterval)`,
  `Start()`, pass it to `middleware.NewRequestLogger(meter)` in the global chain (replacing
  `middleware.RequestLogger`), and `Close` it in the shutdown sequence next to `usageRecorder.Close`.
  A nil meter (no control plane) must behave exactly like today.
Tests: middleware test that an authenticated request reaches the observer with its tenant id and
an unauthenticated one does not; meter tests with a fake writer (aggregation, flush on Close, p95).

## Step 4 — Watch-list rules (pure)
**Files (new):** `usage/rules.go`, `usage/rules_test.go`.

`type RuleInput struct{TenantID string; Active bool; Series map[string][]float64 /* metric key →
daily values oldest-first, last element = the rolled-up day; up to 35 days */; DaysSinceSignIn
*int; Requests24h, Errors5xx24h, FailedJobs24h, DeadJobs24h int64; ComputePerActiveUser float64;
FleetP95ComputePerUser float64; Limits map[string]float64 /* empty today */}`.
`func Evaluate(in RuleInput) []AlertFiring` implementing the spec table exactly, with named consts
for every threshold: spike (compute_*, requests, ai_tokens: last > mean28+3σ AND > 2×median7),
rapid_growth (db_bytes, r2_bytes: ≥ +50% vs 7 days earlier AND ≥ +1 GiB), fleet_outlier
(ComputePerActiveUser > FleetP95ComputePerUser, needs ≥ 1 active user and FleetP95 > 0),
error_rate (Errors/Requests > 5% with ≥ 500 requests → critical), failed_jobs (dead ≥ 1 or
failed ≥ 3), dormant (Active && DaysSinceSignIn ≥ 14 → info; never-signed-in counts),
quota_near/quota_exceeded only when `Limits` has the key. Messages are plain English with units,
e.g. `R2 storage up 211% in 7 days (132 GB → 410 GB)`; add a small pure `formatBytesGB` helper.
`func FleetP95(values []float64) float64` (nearest-rank). Exhaustive table tests incl. threshold
edges, too-short series (no fire), zero baselines, and the small-tenant noise guard.

## Step 5 — Daily rollup
**Files (new):** `usage/rollup.go`, `usage/rollup_sources.go`, `usage/rollup_test.go`.
**Reference:** `automation/runner.go` (TenantLister/PoolSource interfaces, `Servable()`,
bounded concurrency + `safego.Do`, per-tenant timeout, one tenant's failure never stops others).

`type Rollup struct` built by `NewRollup(tenants TenantLister, pools PoolSource, store
rollupStore, r2 r2Measurer, now func() time.Time)` (consumer-side interfaces; `rollupStore` is
the subset of FleetStore it uses; `r2Measurer` = `Measure(ctx, tenantID, bucket string)
(*StorageSnapshot, error)` — add an exported `Measure` method on `StorageRefresher` that calls
its existing private `measure`, so the snapshot table also stays fresh).
`func (r *Rollup) Run(ctx, day time.Time) (RollupSummary, error)` (`RollupSummary{Tenants, Failed int}`):
1. Pass 1, per servable tenant (concurrency const 5, per-tenant timeout const): hourly totals →
   `requests, errors_5xx, p95_ms, compute_request_hours (handler_ms/3.6e6)`; job stats →
   `compute_job_hours, failed_jobs, dead_jobs`; tenant pool → `SELECT pg_database_size(current_database())`
   → `db_bytes`, top 10 tables via `pg_total_relation_size`/`reltuples` on `pg_class` (relkind 'r',
   public schema) → ReplaceTableSizes; tenant `audit_logs` for the day → `records_created.<resource>`
   (action 'create') and `document.sent` count; tenant `automation_deliveries` →
   `automation_runs` (run_at in day, status in the terminal set) and sent count; `emails_sent` =
   document.sent + sent deliveries; R2 via r2Measurer when `tenant.R2Bucket != ""` → `r2_bytes`,
   `r2_objects`; sign-in stats → `active_users`. Write with UpsertDailyMetrics.
2. Fleet: `FleetP95` of compute-per-active-user across tenants with ≥ 1 active user.
3. Pass 2, per tenant: load 35 days of DailyValues + TokensByDay into `RuleInput.Series`, run
   `Evaluate`, `RecordFiring` each, then `ResolveQuiet(quietSince = day - 2 days)` (3 quiet days).
4. `PruneRetention`.
All tenant SQL lives in `rollup_sources.go` and uses only that tenant's pool; all control-plane
SQL goes through the store. Tests: fakes for every interface — failing tenant isolated, idempotent
re-run writes the same values, non-servable tenants skipped, no R2 bucket → no r2 keys.

## Step 6 — Internal rollup endpoint + daily cron (D1)
**Files:** new `controllers/usage_rollup.go`, `controllers/usage_rollup_test.go`; edit
`main.go` (route + deadline-exempt prefix), `route_reachability_test.go` (machine-only allowlist),
`automation-cron/worker.js`, `automation-cron/wrangler.toml`, `automation/CLAUDE.md` (rule 0 note).
**Reference:** `controllers/automation.go` `Tick` (secret check → 404 when unset, body cap,
`middleware.VerifyInternalSignature`, `ReplayGuard.FirstUse`, `logSecurityEvent` on failures).

- `UsageRollupOps` / `NewUsageRollupOps(roller usageRoller, secret string)`; `Rollup` handler for
  `POST /api/internal/usage/rollup`. Same signature scheme and secret (`AutomationTickSecret`),
  **no** platform-switch gate. Body `{}` or `{"day":"yyyy-mm-dd"}` (backfill; must be a past UTC
  day within a const 31-day window, else 400). Default day = yesterday UTC. Responds
  `{success, day, summary}`; 500 with `success:false` on failure.
- `main.go`: register with `automationRateLimiter.PerIPFunc`, only when `cp != nil`; add
  `"/api/internal/usage/"` to `deadlineExemptPrefixes`.
- Worker: add cron `"41 2 * * *"` to `wrangler.toml` and `USAGE_ROLLUP_URL` var
  (`https://stonesuite-backend.fly.dev/api/internal/usage/rollup`); in `scheduled(controller, …)`
  route `controller.cron === "41 2 * * *"` to a signed POST to `USAGE_ROLLUP_URL` **without** the
  KV check; every other cron keeps today's gated tick. Factor the signed POST so both share it.
- `automation/CLAUDE.md` rule 0: append one sentence: the daily usage rollup
  (`/api/internal/usage/rollup`, cron 02:41 UTC) is the single approved exception — metering must
  run while automation is off, and it wakes the machine once a day.
Tests: bad signature 401, replay 401, unset secret 404, bad day 400, happy path calls the roller
with yesterday, backfill day passes through.

## Step 7 — Platform API
**Files:** new `usage/overview.go`, `usage/overview_test.go`, `controllers/platform_usage.go`,
`controllers/platform_usage_test.go`; edit `main.go` (routes). Regenerate API docs if
`route_reachability_test.go` / apidocs tests require it (`go run ./cmd/gen-apidocs`).
**Reference:** `controllers/automation_platform.go` (`platformAdminChecker`,
`requirePlatformAdmin`, 403 + `logSecurityEvent` for non-admins), `tenancy.ControlPlane.ListTenantsPage`.

`usage/overview.go` — pure assemblers from store reads to the exact JSON the frontend expects
(camelCase; bytes, hours; `null` for unmeasured): `TenantUsageRow{tenantId, slug, displayName,
status, activeUsers, totalUsers, computeHours, computeHoursPrev, dbBytes, dbBytesPrev, r2Bytes,
r2BytesPrev, aiTokens, aiTokensPrev, errorRatePct, errorRate24hPct, computeTrend[], topSeverity}`,
`FleetTotals{activeTenants, totalTenants, computeHours, computeHoursPrev, dbBytes, dbBytesPrev,
r2Bytes, r2BytesPrev, aiTokens, aiTokensPrev, computeTrend[]}`, detail `metrics{computeRequestHours,
computeJobHours, dbShareOfFleetPct, r2Objects, aiTokensBySource[{source,totalTokens}], dauMauPct,
lastSignInAt, requests, requestsPrev, p95LatencyMs, failedJobs, deadJobs}`, `series[{date,
computeRequestHours, computeJobHours, dbBytes, r2Bytes}]`, `topTables[{tableName, totalBytes,
rowEstimate}]`, `topTablesMeasuredAt`, `activity{recordsByModule[{label,count}], emailsSent,
automationRuns}`, `alerts[]` as `{id, tenantId, tenantName, ruleKey, metricKey, severity, message,
detail, firstSeenAt, lastSeenAt, status, snoozedUntil}`. Sums over the window; db/r2 = last value in
the window, prev = last value in the prev window; `errorRatePct` = errors/requests over the window,
`errorRate24hPct` = last day; `computeTrend` = daily compute (req+job) for every day in the window
(0-filled); `dauMauPct` = mean daily active_users ÷ active users over 30 days. Period parser
`ParsePeriod(s string) (days int, ok bool)` for 7d|30d|90d. Table tests for every assembler.

`controllers/platform_usage.go` — `PlatformUsageOps` with handlers, all RequireAuth + platform admin:
- `GET /api/platform/usage/overview?period&limit&cursor` — period default 30d (400 on invalid),
  limit default 50, max 200; rows from `ListTenantsPage(limit, cursor)` (cursor passed through
  untouched, `nextCursor` omitted when empty); fleet over **all** tenants (`ListTenants`).
- `GET /api/platform/usage/watchlist`.
- `GET /api/platform/usage/tenants/{tenantId}?period` — unknown tenant → **404**
  `"Usage is not available for this tenant."`; 90-day alert history.
- `POST /api/platform/usage/alerts/{alertId}/ack` — body `{}` or `{"snoozeDays":7|30}`, anything
  else 400; unknown/resolved alert 404; logs `slog.Info("usage alert acknowledged", ...)` with actor.
All errors `{success:false, message}`. Window end = `LatestRolledUpDay` (fallback: yesterday UTC).
Tests: non-admin 403, bad period 400, unknown tenant 404, ack validation, happy-path JSON shape
against a fake store (assert the camelCase keys the frontend reads).

## Step 8 — Docs (orchestrator, not a worker)
Spec already records D1–D3 and the planning adjustments. After implementation: note in the final
report that the Worker must be redeployed manually (`cd automation-cron && wrangler deploy`) for
the daily rollup to start, and that nothing is shown until the first rollup has run.

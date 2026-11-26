# Platform › Tenant Usage — design

Date: 2026-11-26. Frontend: Skookum-Infotech/StoneSuite-WebUI#454 (built against this contract;
`src/types/platformUsage.ts` there is the source of truth for response shapes, with sample data
behind `VITE_PLATFORM_USAGE_MOCK=true` until these endpoints ship).

## Problem
Platform admins have no cross-tenant view of what each tenant consumes, and no way to spot a
tenant whose usage is abnormal (sudden storage growth, error spikes, runaway compute) or
dormant. `usage/` already meters LLM tokens (`tenant_llm_usage_daily`) and the latest R2 bucket
size (`tenant_storage_snapshots`), but only per tenant, for the Subscription page
(`GET /api/tenant/subscription/usage`). Revenue/MRR is out of scope (billing is still a mockup).

## Design

### 1. Metrics and where they come from
| Metric | Source | Notes |
|---|---|---|
| Compute hours (**estimated**) | New request middleware: handler wall time per tenant (tenant from JWT context), plus `jobqueue` job run time | Neon is one project with many databases, so it cannot report compute per tenant. UI always labels this "est." |
| DB load share | Per-tenant DB query time ÷ total | Used to split the shared Neon compute bill |
| Database storage | Daily `pg_database_size('tenant_<slug>')` | `dbShareOfFleetPct` = tenant ÷ sum of all tenants |
| Largest tables | Top 10 by `pg_total_relation_size`, `reltuples` as row estimate | Latest snapshot only |
| R2 storage | Reuse `StorageRefresher` / `tenant_storage_snapshots` | Needs a **daily history row** for trends (the snapshot table keeps latest only) |
| AI tokens | Reuse `tenant_llm_usage_daily` | Same `source` keys as the Subscription page |
| Active users | Distinct sign-ins in period (`activeUsers`) of enabled staff users (`totalUsers`); DAU/MAU %; last sign-in | |
| API traffic | Middleware counters: requests, 5xx, p95 latency | |
| Activity | Records created per workflow module, emails sent, automation runs | |
| Jobs | Failed / dead `jobqueue` jobs in last 24h | |

### 2. Collection pipeline
```
request → usage middleware → in-memory counters per tenant (no DB write per request)
            └─ flush every 60s + on graceful shutdown (shutdown.go) → tenant_request_hourly (one batched UPSERT)
async_jobs (finished) → job time read at rollup (updated_at - created_at)

automation-cron daily cron 02:41 UTC → POST /api/internal/usage/rollup (HMAC, not gated by the automation switch; rolls up yesterday UTC):
  1. tenant_request_hourly → tenant_usage_daily
  2. per tenant DB: pg_database_size + top tables (concurrency ≈ 5)
  3. R2 bytes/objects via StorageRefresher → tenant_usage_daily
  4. active users, records created, emails, LLM tokens → tenant_usage_daily
  5. evaluate watch-list rules → upsert tenant_usage_alerts
```
Scale-to-zero rules out an in-process daily ticker, hence the daily cron call (see D1). The rollup is
idempotent per (tenant, day), so a retried or doubled tick is harmless.

### 3. Tables (control plane, appended with `IF NOT EXISTS`, every query filters by `tenant_id`)
- `tenant_request_hourly(tenant_id, hour, requests, errors_5xx, handler_ms, p95_ms)` — 90-day retention.
- `tenant_usage_daily(tenant_id, day, metric_key, value NUMERIC, PRIMARY KEY (tenant_id, day, metric_key))` — 13-month retention; long-form so a new metric needs no migration.
- `tenant_table_sizes(tenant_id, measured_at, table_name, total_bytes, row_estimate)` — latest snapshot.
- `tenant_usage_alerts(id, tenant_id, rule_key, metric_key, severity, observed, baseline, message, detail, first_seen_at, last_seen_at, status, acted_by, snoozed_until)` — `status` ∈ `open|acked|snoozed|resolved`.

### 4. Watch-list rules (evaluated after each daily rollup)
| `ruleKey` | Fires when | `severity` |
|---|---|---|
| `quota_exceeded` / `quota_near` | usage ≥ 100% / ≥ 80% of a plan limit (only when one is set) | critical / warning |
| `spike` | today > 28-day mean + 3σ **and** > 2× 7-day median | warning |
| `rapid_growth` | DB or R2 storage ≥ +50% week over week **and** ≥ +1 GB | warning |
| `fleet_outlier` | compute per active user above the fleet's 95th percentile | warning |
| `error_rate` | 5xx > 5% over 24h with ≥ 500 requests | critical |
| `failed_jobs` | any dead job, or ≥ 3 failed in 24h | warning |
| `dormant` | active tenant with no sign-ins for 14 days | info |

Each alert stores observed + baseline and a plain-English `message`
(e.g. "R2 storage up 211% in 7 days (132 GB → 410 GB)"). `metricKey` ∈ `compute, db, r2,
ai_tokens, users, requests, error_rate, jobs` flags the matching card in the UI. Ack or snooze
7/30 days; an alert auto-resolves after its rule has not fired for 3 consecutive days.

### 5. API (`/api/platform/` — RequireAuth + `IsPlatformAdmin`, no tenant chain, same guard as `controllers/automation_platform.go`)
Sizes in bytes, compute in hours, ISO-8601 timestamps, series days `yyyy-mm-dd` UTC. Unmeasured
metrics are `null`, never `0`. "Prev" = the previous period of equal length. Errors follow the
usual `{ success: false, message }`.

**`GET /api/platform/usage/overview?period=7d|30d|90d&limit=&cursor=`**
```jsonc
{ "success": true, "generatedAt": "…", "period": "30d",
  "fleet": { "activeTenants": 8, "totalTenants": 10,
             "computeHours": 175.2, "computeHoursPrev": 168.0,
             "dbBytes": 0, "dbBytesPrev": 0, "r2Bytes": 0, "r2BytesPrev": 0,
             "aiTokens": 0, "aiTokensPrev": 0, "computeTrend": [5.1, 5.6] },   // same on every page
  "tenants": [ /* TenantUsageRow */ ],
  "nextCursor": "opaque" }                                                    // keyset; omitted on last page
```
`TenantUsageRow`: `tenantId, slug, displayName, status, activeUsers, totalUsers, computeHours,
computeHoursPrev, dbBytes, dbBytesPrev, r2Bytes, r2BytesPrev, aiTokens, aiTokensPrev,
errorRatePct, errorRate24hPct, computeTrend[] (daily, oldest first), topSeverity
(critical|warning|info|null)`.

**`GET /api/platform/usage/watchlist`** → `{ success, alerts: UsageAlert[] }` — open alerts plus
snoozed ones past `snoozedUntil`. `UsageAlert`: `id, tenantId, tenantName, ruleKey, metricKey,
severity, message, detail?, firstSeenAt, lastSeenAt, status, snoozedUntil?`.

**`GET /api/platform/usage/tenants/{tenantId}?period=`** → `{ success, generatedAt, period,
tenant: TenantUsageRow, metrics, series, topTables, topTablesMeasuredAt, activity, alerts }`
- `metrics`: `computeRequestHours, computeJobHours, dbShareOfFleetPct, r2Objects,
  aiTokensBySource[{source,totalTokens}], dauMauPct, lastSignInAt, requests, requestsPrev,
  p95LatencyMs, failedJobs, deadJobs`
- `series[]`: `{ date, computeRequestHours, computeJobHours, dbBytes, r2Bytes }` per day
- `topTables[]`: `{ tableName, totalBytes, rowEstimate }`, largest first
- `activity`: `{ recordsByModule[{label,count}], emailsSent, automationRuns }`
- `alerts[]`: all alerts from the last 90 days, any status
- Unknown **or** not-visible tenant → **404** (UI shows a neutral "not available" message).

**`POST /api/platform/usage/alerts/{alertId}/ack`** body `{}` (acknowledge) or
`{ "snoozeDays": 7 | 30 }` → `{ success: true }`. Validate `snoozeDays`; log the action with
the acting user id.

## Decisions (approved 2026-11-26)
- **D1 Rollup trigger — its own daily call, not gated by the automation switch.** The
  `automation-cron` Worker gets a second, **daily** cron (`41 2 * * *`) that signs
  `POST /api/internal/usage/rollup` with the same `AUTOMATION_TICK_SECRET`. It is not gated by
  `/api/platform/automation`, so metering keeps running while automation is off, and it wakes
  the scale-to-zero machine and Neon **once a day**, not every 15 minutes. This is a deliberate,
  documented exception to `automation/CLAUDE.md` rule 0 ("don't add a second scheduler path"),
  which is amended to say so. The 15-minute tick is unchanged.
- **D2 R2 history.** The rollup re-measures each bucket via `StorageRefresher` and writes
  `r2_bytes` / `r2_objects` rows to `tenant_usage_daily`; `tenant_storage_snapshots` is
  unchanged for the Subscription page.
- **D3 Compute estimate.** Request handler wall time (metered) + background-job time
  (`async_jobs.updated_at - created_at` for finished jobs, so it includes queue wait), labelled
  "est." in the UI. Revisit if Neon moves to per-tenant projects.

### Adjustments made while planning
- **DB load share is deferred.** Measuring per-tenant DB query time needs a pgx tracer on every
  pool; out of scope for v1. `dbShareOfFleetPct` is the tenant's share of total tenant DB
  **size** instead.
- **Request attribution.** `RequireAuth` stores its payload on the inner request context, so the
  outer `RequestLogger` never sees the tenant (its `tenant_id` log field is currently always
  empty). A per-request attribution slot set by `RequestLogger` and filled by `RequireAuth`
  fixes both the meter and that log field.
- **Activity sources.** Records created = tenant `audit_logs` rows with `action = 'create'`,
  grouped by `resource`. Emails sent = `audit_logs` `document.sent` rows + `automation_deliveries`
  with `status = 'sent'`. Automation runs = `automation_deliveries` whose `run_at` falls in the
  day and status is terminal. Active users / last sign-in = control-plane `auth_sessions` joined
  to `identities` (`last_seen_at` / `created_at`); total users = the tenant's `identities`.

## Testing
Table tests for every rule evaluator (threshold edges, small-tenant noise guard, snooze expiry,
auto-resolve), the period/prev-period window math and the overview cursor; middleware counter
flush (including on shutdown) with a fake writer; DB-tagged tests for the rollup UPSERTs and
per-tenant size queries; handler tests for the platform-admin guard (403 for non-admins), 404 on
unknown tenant, and ack/snooze validation.

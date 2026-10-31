# StoneSuite Backend — Claude Memory

## What This Service Is
Go backend for **StoneSuite**, a multi-tenant, white-label CRM on database-per-tenant architecture: dynamic workflow engine, custom fields (≤15 per workflow, JSONB), dynamic RBAC (resource + action + scope), multi-user workspaces, central auth (email/password + JWT + OAuth/SAML SSO). Frontend is a separate repo ([Skookum-Infotech/StoneSuite](https://github.com/Skookum-Infotech/StoneSuite)); this repo is the sole source of truth for backend code.

**Subsystem rules live next to the code** in nested `CLAUDE.md` files (loaded when you read files there): `approvalchain/` (notifications), `globalsearch/`, `automation/`, `domainevent/`, `proposedaction/`, `docextractjob/`, `query/`, `ai/`, `workflow/` (custom fields), `database/migrations/`, `deployment/` (Fly + secrets). Read the relevant one before changing that subsystem.

## Repo Structure
- `main.go` entry (init control plane, migrations, routes); `config/` env-based `AppConfig`
- `tenancy/` control-plane registry/resolver/middleware; `authz/` RBAC catalog + enforcer; `middleware/` JWT, tenancy, logging, recover, rate limit
- `controllers/` HTTP handlers + route wiring; `userstore/`, `crmstore/` (v1 JSONB + v2 relational), `workflow/` engine, `query/` filter engine
- Document modules (clone twins): `quote estimate salesorder invoice payment creditmemo refund vendors`, purchasing `purchaseorder requisition itemreceipt vendorbill vendorcredit vendorpayment expense`, `fabrication orderform portal`, inventory `inventory inventoryadjustment inventorycount inventorytransfer bintransfer`, accounting `chartofaccounts accountingperiod journal cashtransfer`
- Cross-cutting: `approvalchain/`, `globalsearch/`, `docflow docpdf docextract docextractjob importer recordemail`, `automation/` (+ `automation-cron/` Worker), `domainevent proposedaction provenance proposedentity`, `jobqueue/`, `provisioning/`, `services/` (email/notify), `storage/` (R2), `secret/`, `metrics logship`, `saml/`
- `ai/` provider-agnostic RAG (dep-free); `ollama/` its own Fly app; `cmd/` CLIs; `docs/` embedded help corpus
- Small helpers: `duplicate kitref linedim dateonly dbtestutil cache models`; tenant settings: `companyprofile companylocation onboardingseed aisettings dashboardui feedback auditstore crmactivity customernote mytransactions`
- `database/migrations/{control_plane,tenant}/schema.sql` — canonical schemas

## Common Commands
```bash
go run .                  # start server → http://localhost:8080
go test ./...             # run all tests
go test -tags dbtest ./... # run DB-backed tests too (needs TEST_DATABASE_URL; skip cleanly otherwise)
go build ./...             # verify build compiles
golangci-lint run          # lint (requires golangci-lint installed)
```

## Feature Pipeline

`/feature <description>` (`.claude/commands/feature.md`) drives a feature end-to-end:
classify → design + plan → **approval gate** → implement → verify → conditional review
fan-out → capped fix loop → report. It stops with a dirty tree and a suggested commit
message; it never commits, pushes, or opens a PR.

| Stage | Runs as | Model |
|---|---|---|
| Orchestrate, design, plan, triage | main session | Opus 4.8 |
| Implement a plan step / apply fixes | `go-implementer` | Sonnet 5 |
| Tests + consolidated report | `go-verifier` | Sonnet 5 |
| Drift / tenancy / migration / filter checks | the 4 existing agents | haiku |

Design/plan/orchestration is `superpowers` — do **not** build parallel design, planner, or reviewer agents.
**Push each new check to the cheapest layer that can catch it:** hooks (`.claude/hooks/`) → CI → haiku agents (`.claude/agents/`) → skills (`.claude/skills/`) → superpowers → `/feature`.
A guard that fires on known-good content gets ignored (scope whole-file checks to what is *never* legitimate, diff-scope the rest); write agent rules against measured drift, not principle.

## Deployment (summary — details in `deployment/CLAUDE.md`)
Fly.io (single Go app, `iad`, scale-to-zero) + Neon Postgres. `deploy-backend.yml` deploys on merge to `master` (test → deploy → health check → auto-rollback); `ci-backend.yml` gates PRs. `fly.toml` is source-controlled — never edit deploy config in the dashboard. `CORS_ORIGIN`/`FRONTEND_URL` must match the frontend origin exactly. **Never use down-migrations** — recover via Neon PITR/branching.

## Go Rules (always enforce)
- Package names: lowercase, single word, no underscores.
- Errors: always check and wrap — `fmt.Errorf("context: %w", err)`. Never swallow.
- No `panic()` in production paths — return errors up the call stack.
- Struct fields: PascalCase (exported) for JSON marshaling; lowercase for internal.
- Interfaces: define at point of use (consumer side), not at implementation.
- HTTP handlers: `func (h *Handler) Name(w http.ResponseWriter, r *http.Request)`.
- Service/DB functions: `context.Context` as first parameter.
- All config via env vars — never hardcode values; use `config.AppConfig`.
- No global mutable state — inject dependencies via constructor.
- Tests: `testify`, table-driven for all pure functions.
- Goroutines: must have explicit exit strategy (WaitGroup or channel).

## Strict Implementation Rules (CRITICAL)

### Multi-Tenancy (Inviolable)
1. **Every query is tenant-scoped by construction.** No `WHERE tenant_id` filters. Instead: separate databases (`tenant_<slug>`) — the DB connection itself is the scope; in control-plane queries, always explicitly filter by `identity.tenant_id` or `tenant.id`.
2. **Never select/join across tenant databases.** Tenants are fully isolated.
3. **TenantResolver middleware is MANDATORY** on all tenant-scoped routes. Missing it = security bug.
4. **JWT carries `tenant_id` + `user_id` + `identity_id`.** Every handler gets these from context via `TenantFromContext()`, `UserFromContext()`, etc.

### RBAC (Permission Enforcement)
1. **Every mutation (POST/PATCH/DELETE) must check `Require(resource, action)`** before executing.
2. **Every list/read (GET) must apply scope filtering** (`all|own`) from the caller's roles. The scope model is two-level and fail-closed — the `team` scope was retired in `2dd211f`, and anything unrecognized narrows to `own`.
3. **Single-record access must enforce ownership scope, not just the permission (IDOR guard).** Use `recordInScope(ctx, pool, scope, identityID, ownerUserID)` (controllers/scope.go:29) on every single-record GET/PATCH/DELETE/transition. CRM record handlers get this for free via `authCRMByRecordID`; `WorkflowOps` uses `enforceRecordScope`. On scope denial return **404** (not 403) so ids can't be enumerated.
4. **No permission bypass.** If a handler ever skips the enforcer, it's a bug.
5. **System roles (super_admin, guest) are immutable** — cannot be deleted or modified by users.
6. **Log security events.** Failed logins, permission denials, IDOR attempts, and rate-limit hits go through `logSecurityEvent(r, "<event>", kv...)` or `slog.Warn("security event", ...)` with a stable `security_event` key. Never log passwords or raw tokens.

### Document Modules (Clone Discipline)
The relational document modules — `quote`, `estimate`, `salesorder`, `invoice`, `payment`, `creditmemo`, `refund`, `vendors` — are structural **copy-paste twins** cloned from one skeleton (package + `tenant/schema.sql` tables + controller wired in `controllers/` + RBAC catalog entries + status/approval flow). There is no shared base, so a cross-cutting fix (auth chain, scope plumbing, `logSecurityEvent`, registration) must be **hand-ported to every module**.
1. **Scaffold new modules with the `new-module` skill** — it wires the full security chain; don't hand-roll.
2. **After editing any module, run the `module-drift-checker` agent** to catch copy-paste leftovers and missing auth/scope/logging.
3. A module's `store.go` should be split by verb (`store_create.go`/`store_update.go`/`store_transition.go`/`store_search.go` or `search.go`, naming varies by module vintage) once it grows large; `invoice/store_line_resolve.go` is the reference pattern for pulling out shared line-resolution helpers. Some existing "clean" siblings (`quote`, `estimate`) still exceed the 300-line file cap — known pre-existing drift, not a new problem to fix reflexively.

### Cross-package tripwires (details in the named package's CLAUDE.md)
These apply while editing *other* packages, so they stay here:
- **Registration.** A module is notification-dead until registered in `approvalchain/registry.go` (non-empty `DisplayName`), search-dead until in `globalsearch/registry.go` + `registry_test.go`, and AI-dead without an `addAI(...)` entry in `globalsearch/ai_<domain>.go`. `new-module` covers all three.
- **Transitions** must call a `notifyTransition` hook after a successful status change (`invoice/store_transition.go` is the reference).
- **Emails.** Set `ActorUserID` on every `services.NotificationRequest`; go through `services/notify.go`, never inline notify HTTP in `controllers/`; a handler sending a transactional email returns `"emailSent": bool`, logs failures via `slog`, and persists notify ids. This backend **never talks to Resend** (inbound included).
- **Record filtering** goes only through `query/`; filter ⨯ scope is ANDed, keys are whitelisted, values parameterized, `*query.InvalidFilterError` → 400.
- **Automation SQL** never uses `CURRENT_DATE` (tenant-local "today" computed in Go); automation writes run under `automation.WithSystemActor(ctx)`; outbound sends carry an `IdempotencyKey`.
- **Domain events:** `domainevent.Emit` takes the store's transaction, never the pool. **ProposedAction** executors call existing store paths; a new module create path used by them needs a `CreateTx` twin.
- **AI/RAG:** `ai/` stays dependency-free of app packages; LLM + embeddings are self-hosted Ollama — no hosted API without an explicit decision. A user-visible feature change must update the matching help doc in `docs/`.
- **Custom fields** validate against `workflow_field_definitions` before save, max 15 per workflow, never hand-craft JSONB.
- **Schema changes:** append with `ADD COLUMN IF NOT EXISTS`, never edit an existing `CREATE TABLE` body, guard `ADD CONSTRAINT`, no destructive DDL — use `/add-migration` and read `database/migrations/CLAUDE.md`.

### API & HTTP
1. **Routes are prefixed by scope:** `/api/` general, `/api/auth/` auth, `/api/platform/` platform admin, `/api/onboarding/` public, `/api/tenant/` tenant-scoped (auth + TenantResolver required), `/api/internal/` machine-only (HMAC-signed via `middleware.VerifyInternalSignature`, no JWT — currently just the `automation/` tick the `automation-cron` Cloudflare Worker calls every 15 min, since scale-to-zero rules out in-process tickers; that tick is itself gated by the platform switch at `/api/platform/automation`).
2. **All responses are JSON.** Status codes: 200, 400, 401, 403, 404, 409, 500.
3. **Error responses always include `success: false, message: "..."`.**
4. **Handlers must validate input.**

### Goroutines & Async (Reliability)
1. **Every goroutine must have an explicit exit strategy** (WaitGroup, channel, context cancellation).
2. **Long-running jobs (provisioning, migration fan-out) must be resumable.**
3. **Provisioning jobs are enqueued atomically.** All-or-nothing.
4. **No `defer` without checking error.**

### Observability & Middleware
1. **Structured logging only** (`log/slog`, JSON handler). No new `log.Printf` in request paths.
2. **Every request is correlated** via `middleware.RequestLogger` (request id, one line per response).
3. **Panics never crash the VM** — `middleware.Recover` wraps all routes.
4. **Global chain is `RequestLogger(Recover(Deadline(corsHandler)))`.** `Deadline` cancels the request context after `REQUEST_TIMEOUT` (110s; must stay between the 100s Ollama client timeout and the 120s `WriteTimeout`); only self-bounded routes (`/ask/stream`, the automation tick) are exempt. Every pool connection also carries `DB_STATEMENT_TIMEOUT`/`DB_LOCK_TIMEOUT`/`DB_IDLE_IN_TX_TIMEOUT` (`tenancy.SessionTimeouts`); schema migrations lift the first two with `SET LOCAL`.
5. **Unauthenticated credential endpoints are per-IP rate-limited.** Use `middleware.ClientIP(r)` for IP keying.
6. **`GET /api/healthz`** liveness, **`GET /api/readyz`** readiness (control-plane DB ping), **`GET /api/metrics`** Prometheus.
7. **Error tracking + log shipping are optional + graceful** (`SENTRY_DSN`, `AXIOM_TOKEN`+`AXIOM_DATASET`). The log shipper must never call `slog` (infinite loop).

### Code Quality
1. **No magic strings or numbers.** Constants for anything > 1.
2. **Errors are wrapped with context.**
3. **All public functions are documented** (single-line comment above declaration).
4. **Types are never `any`.**
5. **Table-driven tests for all pure functions** (`testify/assert`).

## General Rules
- **Commits:** Conventional Commits — `feat:`, `fix:`, `refactor:`, `chore:`, `docs:`. Be specific.
- **Never commit `.env`, `*.key`, `credentials.json`, or secrets.**
- **New features need tests.** Tests must pass locally and in CI before merging to `develop`.
- **Files over 300 lines: split them.**

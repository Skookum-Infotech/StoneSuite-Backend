# StoneSuite Backend — Claude Memory

## What This Service Is
Go backend for **StoneSuite**, a multi-tenant, white-label dynamic CRM platform built on database-per-tenant architecture. Each customer org gets a fully isolated database with a dynamic workflow engine, custom fields (≤15 per workflow, JSONB), dynamic RBAC (resource + action + scope), multi-user workspaces, and central auth (email/password + JWT + OAuth SSO via Entra ID/Cognito/Okta).

The frontend (React 19 + TypeScript + Vite) lives in a separate repo: [Skookum-Infotech/StoneSuite](https://github.com/Skookum-Infotech/StoneSuite). This repo was split out of that monorepo's `backend/` directory on 2026-07-02, with git history preserved; treat it as the sole source of truth for backend code going forward.

## Repo Structure
```
.
├── fly.toml                 # Fly.io deploy config (v2); ollama/ ships its own fly.toml
├── Dockerfile               # Multi-stage Alpine build (CGO_ENABLED=0)
├── main.go                  # Entry point: init CP, apply migrations, wire routes, start server
├── config/                  # Env-based AppConfig struct
├── tenancy/                 # Control-plane registry, resolver, router, middleware
├── authz/                   # RBAC: permission catalog, enforcer
├── workflow/                # Workflow engine: state machines, custom fields, seed data
├── crmstore/                # v1 JSONB + v2 relational CRM record stores
├── query/                   # Store-agnostic record filter/sort/keyset-pagination engine
├── controllers/             # HTTP handlers + module route wiring (tenant, CRM, RBAC, onboarding)
├── middleware/              # JWT auth, tenancy resolver, request logging, recover, rate limiting
├── userstore/               # Tenant user store
├── quote/ estimate/ salesorder/ invoice/ payment/ creditmemo/ vendors/
│                            # Relational document modules (clone twins) — see "Document Modules"
├── inventory/               # Inventory items module (relational)
├── ai/                      # Provider-agnostic RAG primitives (Embedder/LLMClient, orchestrator); dep-free of app pkgs
├── ollama/                  # Self-hosted Ollama LLM — its own Fly app (Dockerfile/fly.toml/entrypoint)
├── cmd/rag-ingest-help/     # CLI: ingest embedded help docs into the RAG index
├── jobqueue/                # Async job queue
├── provisioning/            # Provisioner: queue, worker, async job runner
├── services/                # Email service, provisioning helpers
├── storage/                 # R2 per-tenant bucket client
├── secret/                  # Field-level secret encryption (SECRET_ENCRYPTION_KEY)
├── cache/ models/           # In-process cache; shared model types
├── metrics/ logship/        # Prometheus metrics, Axiom log shipping
├── docs/                    # Embedded help-doc corpus (docs/embed.go) + concept notes
└── database/migrations/
    ├── control_plane/schema.sql  # Canonical CP schema: tenants, identities, invites, sso_configs, audit_logs
    └── tenant/schema.sql         # Canonical tenant template: workflows, states, transitions, records, module tables, audit_logs
```

## Common Commands
```bash
go run .                  # start server → http://localhost:8080
go test ./...             # run all tests
go build ./...             # verify build compiles
golangci-lint run          # lint (requires golangci-lint installed)
```

## Deployment (Fly.io)

**Stack:** Fly.io (single Go app, `iad` region, **scale-to-zero** — the Machine stops when idle and auto-starts on the next request, ~1-2s cold start; costs nothing at idle) + Neon Postgres (single project, ~30 tenant databases + control-plane DB).

**Deploy pipeline:** `.github/workflows/deploy-backend.yml` runs on every PR merged to `master`: test → `flyctl deploy` → health-check `GET /api` → auto-rollback on failure. `.github/workflows/ci-backend.yml` gates PRs and pushes to `develop`/`master` with build/vet/test + `govulncheck`.

**One-time setup:**
```bash
brew install flyctl
fly auth login
```

**Secrets** (`fly secrets set ...`): `CONTROL_PLANE_DB_URL`, `PROVISION_ADMIN_DB_URL`, `JWT_SECRET`, `SECRET_ENCRYPTION_KEY`; optional `SMTP_HOST`/`SENDER_EMAIL`/`SENDER_PASSWORD`, `R2_ACCOUNT_ID`/`R2_ACCESS_KEY_ID`/`R2_SECRET_ACCESS_KEY`/`CLOUDFLARE_API_TOKEN`, `SENTRY_DSN`, `METRICS_TOKEN`, `AXIOM_TOKEN`/`AXIOM_DATASET`.

**GitHub Actions secret required:** `FLY_API_TOKEN` (repo secret, `environment: Prod`) — needed for `deploy-backend.yml` to run `flyctl deploy`.

**Manual deploy:**
```bash
fly deploy
fly logs
fly status
```

`fly.toml` is source-controlled — never edit deploy config via the Fly dashboard. `CORS_ORIGIN`/`FRONTEND_URL` must match the deployed frontend's exact origin (Cloudflare Pages URL); a mismatch blocks all browser API calls with CORS errors.

**Never use down-migrations** — recovery is via Neon point-in-time restore or branching.

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
2. **Every list/read (GET) must apply scope filtering** (`all|team|own`) from the caller's roles.
3. **Single-record access must enforce ownership scope, not just the permission (IDOR guard).** Use `recordInScope(ctx, pool, scope, identityID, ownerUserID, teamID)` (controllers/scope.go) on every single-record GET/PATCH/DELETE/transition. CRM record handlers get this for free via `authCRMByRecordID`; `WorkflowOps` uses `enforceRecordScope`. On scope denial return **404** (not 403) so ids can't be enumerated.
4. **No permission bypass.** If a handler ever skips the enforcer, it's a bug.
5. **System roles (super_admin, guest) are immutable** — cannot be deleted or modified by users.
6. **Log security events.** Failed logins, permission denials, IDOR attempts, and rate-limit hits go through `logSecurityEvent(r, "<event>", kv...)` or `slog.Warn("security event", ...)` with a stable `security_event` key. Never log passwords or raw tokens.

### Document Modules (Clone Discipline)
The relational document modules — `quote`, `estimate`, `salesorder`, `invoice`, `payment`, `creditmemo`, `vendors` — are structural **copy-paste twins** cloned from one skeleton (package + `tenant/schema.sql` tables + controller wired in `controllers/` + RBAC catalog entries + status/approval flow). There is no shared base, so a cross-cutting fix (auth chain, scope plumbing, `logSecurityEvent`, registration) must be **hand-ported to every module**.
1. **Scaffold new modules with the `new-module` skill** — it wires the full security chain; don't hand-roll.
2. **After editing any module, run the `module-drift-checker` agent** to catch copy-paste leftovers and missing auth/scope/logging.

### Record Filter Engine (`query/`)
The `query` package is the **single, store-agnostic** way to do server-side filtering, sorting, and keyset pagination on records. Both record-list designs (v1 JSONB `workflow.ListRecordsFiltered`, v2 relational `relationalStore.SearchRecords`) route through it. Do not hand-roll record filtering elsewhere.
1. **Filter ⨯ scope is ANDed, never OR.** The RBAC scope clause and the user filter compose with `AND` — a filter can only narrow the caller's permitted set, never widen it. Keep `workflow/filter_test.go` green.
2. **Field keys are a whitelist via `query.FieldResolver`.** An unresolved key is `400` (`*query.InvalidFilterError`), never raw SQL.
3. **All values are parameterized** (`$n`). Never interpolate client values.
4. **Pagination is keyset (opaque base64 cursor), not offset.** Page size caps at `MaxLimit` (100), default 25.
5. **Sortable fields are restricted** to stable non-null columns (`created_at`, `updated_at`, `record_number`).
6. **Map errors correctly.** `*query.InvalidFilterError` → 400, never a 500.
7. **`query` imports nothing app-specific** — keep it dependency-free.

### AI / RAG (`ai/`)
1. **`ai/` is provider-agnostic and dependency-free of app packages** (Embedder + LLMClient interfaces, RAG orchestrator, record rendering) — same discipline as `query/`. Keep app types out of it.
2. **LLM + embeddings are self-hosted via Ollama** (`ollama/` is its own Fly app), not a hosted API — chosen for data-residency/security. Don't swap in a hosted embeddings/LLM API without that trade-off being decided explicitly.
3. **Help corpus is embedded** (`docs/embed.go`) and ingested with `go run ./cmd/rag-ingest-help`; reindex via `POST /api/platform/ai/reindex-help`.
4. **Tenant RAG Q&A is `POST /api/tenant/ai/ask`** (auth + TenantResolver); `/api/embeddings` + `/api/chat` proxy the model.

### Custom Fields & JSONB (Data Integrity)
1. **custom_fields JSONB must validate against `workflow_field_definitions` before save** (type, required, enum, regex).
2. **Max 15 custom fields per workflow** — enforce in validator before INSERT/UPDATE.
3. **Never manually craft JSONB.** Use helpers to build `map[string]any`, validate, then marshal.

### Database & Migrations
1. **Control-plane migrations are idempotent.** Use `CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`.
2. **Tenant migrations never use `ALTER TABLE` to add columns.** Use migrations instead.
3. **Tenant `schema_version` table tracks all applied versions.**
4. **Never run raw SQL in handlers.** Always use prepared statements (pgx named params).
5. **All queries accept `context.Context` as first parameter.**

### API & HTTP
1. **Routes are prefixed by scope:** `/api/` general, `/api/auth/` auth, `/api/platform/` platform admin, `/api/onboarding/` public, `/api/tenant/` tenant-scoped (auth + TenantResolver required).
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
4. **Global chain is `RequestLogger(Recover(corsHandler))`.**
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

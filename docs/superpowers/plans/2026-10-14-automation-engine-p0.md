# Automation Engine P0 (Foundations) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the automation engine's foundations on top of the tick infrastructure that #361 already merged: a multi-observer audit hook carrying the actor, a tenant send window and a system-actor marker, the automation tables, the `automation` RBAC resource, and notify idempotency. These are what P1's reminder jobs build on.

**Architecture:** Everything plugs into the existing `automation/` package. `automation.Runner` already fans a signed tick out to servable tenants (bounded concurrency 4, per-tenant advisory lock, per-tenant timeout, tenant-local `TenantToday`) and runs each registered `Job`. P0 adds no jobs; it adds the primitives and tables P1's `plan`/`drain` jobs need, and makes retried sends safe end to end.

**Tech Stack:** Go, pgx v5 (`pgxpool`), testify, Postgres (Neon), stonesuite-notify (Go, separate repo at `../stonesuite-notify`).

**Spec:** `docs/superpowers/specs/2026-10-14-automation-engine-design.md`

## Baseline: already on develop (#361, `9094100`); do not rebuild
| Spec P0 item | Where it already lives |
|---|---|
| Signed tick endpoint | `POST /api/internal/automation/tick`, `controllers/automation.go` (HMAC over method+path+ts+body via `middleware.VerifyInternalSignature`, 5-min skew, `ReplayGuard`, 404 while the secret is unset, own per-IP limiter) |
| Tenant fan-out + lock | `automation.Runner` (`automation/runner.go`): `maxConcurrentTenants = 4`, `perTenantTimeout = 2m`, `pg_try_advisory_lock`, unlock on `context.Background()` |
| Tenant-local today | `automation.TenantToday(now, tz, fallback)` (`automation/today.go`), fed by `companyprofile.Get` |
| Config | `AUTOMATION_TICK_SECRET` (≥32, validated), `AUTOMATION_DEFAULT_TIMEZONE` (default `America/Chicago`, validated) |
| Cron Worker | `automation-cron/` (`worker.js`, `wrangler.toml`) |
| Job registry | `automation.Job` + `automation.DefaultJobs()` (currently `VendorPaymentDispatch`, which fixes pre-existing bug #4 in the spec) |

## Deviations from the spec (confirm at review)
1. **`async_jobs.run_at` / backoff dropped.** The work runs inside the tick request against tenant-DB tables (`automation_deliveries` carries its own `run_at`/`lease_until`), so nothing in the automation path uses `async_jobs` (YAGNI).
2. **No `users` row for the system actor.** A seeded `users` row would leak into about 10 `FROM users` query sites (user lists and search, seat counts, pickers, crm_admin, mytransactions, authz). Instead:
   - Automation marks its context with `automation.WithSystemActor(ctx)`, and the loop guard checks `automation.IsSystemActor(ctx)`.
   - Employee-keyed columns use the already-seeded `employee_id = 1` (`automation.SystemEmployeeID`).
   - User-keyed actor fields (`audit_logs.actor_user_id`, notify `ActorUserID`) stay empty, with `{"actor":"automation","rule":"<key>"}` in the audit `extraMeta`.
   - Employee 1 is not renamed.
3. **Notify idempotency travels in the JSON body (`idempotencyKey`), not a header.** The backend splits one request per recipient (`services/notify.go` `personalize`), so notify derives a per-recipient key and enforces it with a unique partial index. No 7-day expiry; a key is unique for good.
4. **Reuse #361's `Runner`/`Job` instead of a new `Engine`/`Phase`, and its existing config instead of `AUTOMATION_ENABLED`/budget/concurrency settings.** An unset `AUTOMATION_TICK_SECRET` already disables the engine (404). The `Job.Run(ctx, pool, today)` signature is extended in **P1**, when sends first need `now`, the tenant location and a notifier. P0 changes nothing there.

## Global Constraints
- Every tenant DDL is appended to `database/migrations/tenant/schema.sql` and must be idempotent (`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`, `ON CONFLICT DO NOTHING`). Never edit an existing `CREATE TABLE` body. No `DROP`, rename, or bare `ADD CONSTRAINT` (CHECKs inside a brand-new `CREATE TABLE` body are fine).
- Errors are wrapped `fmt.Errorf("context: %w", err)`. No `panic` in production paths. Use `slog` only; no new `log.Printf`.
- Package names are lowercase single words. Interfaces are defined at the consumer. No global mutable state beyond the existing audit-observer registry (justified there by the lack of a DI seam).
- Tests use testify and are table-driven for pure functions. DB tests use `//go:build dbtest` and skip when `TEST_DATABASE_URL` is unset.
- Send window `[08:00, 18:00)` tenant-local. Files stay under 300 lines.

## Review Focus
1. **A panicking audit observer must not break the other observers or the write.** RAG and automation share the hook now. Pinned in Task 1 (`TestLogAuditFull_ObserverPanicIsolated`).
2. **Send window across DST.** On 2026-03-08 (02:00 doesn't exist) and 2026-11-01 (01:00 happens twice), `NextSendWindow` must return 08:00 local, never skip or repeat a day. Pinned in Task 2.
3. **Schema idempotency.** A second boot of `tenant/schema.sql` must be a no-op. Pinned in Task 3 (dbtest applies the schema twice) and the `migration-auditor` run.
4. **No role silently gains `automation` rights.** Only super_admin, via its `*` wildcard. Pinned in Task 4.
5. **Notify replay sends nothing.** A replayed create returns the original ids and skips `enqueueDeliveries`. Pinned in Task 5.

---

## File Structure
| File | Responsibility |
|---|---|
| `workflow/audit.go` (modify) | `AuditEvent`, multi-observer registry, panic-isolated dispatch |
| `rag_loader_router.go`, `main.go` (modify) | RAG observer adapted to `AuditEvent`, registration via `RegisterAuditObserver` |
| `automation/today.go` (modify) | Extract `TenantLocation`; `TenantToday` delegates to it |
| `automation/window.go` (new) | `InSendWindow`, `NextSendWindow` |
| `automation/actor.go` (new) | `WithSystemActor`, `IsSystemActor`, `SystemEmployeeID` |
| `database/migrations/tenant/schema.sql` (modify) | Automation tables |
| `authz/catalog.go` (modify) | `ResourceAutomation` + catalog rows |
| `../stonesuite-notify/...` + `services/notify.go` | Idempotency key end to end |
| `CLAUDE.md` (modify) | Automation Engine rules |

---

### Task 1: Multi-observer audit hook carrying the actor

**Files:**
- Modify: `workflow/audit.go` (whole observer section, lines 10-27 and 70-72)
- Modify: `rag_loader_router.go:49` (signature)
- Modify: `main.go:279`
- Test: `workflow/audit_observer_test.go` (new)

**Interfaces:**
- Produces: `type AuditEvent struct{ Action, Resource, ResourceID, ActorUserID string }`, `type AuditObserver func(ctx context.Context, q Querier, ev AuditEvent)`, `func RegisterAuditObserver(o AuditObserver)`. `SetAuditObserver` is removed.

- [ ] **Step 1: Confirm the only caller of the old API**

Run: `grep -rn "SetAuditObserver" --include='*.go' --exclude-dir=.claude --exclude-dir=vendor .`
Expected: only `main.go:279` and `workflow/audit.go`.

- [ ] **Step 2: Write the failing test** at `workflow/audit_observer_test.go`

```go
package workflow

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditStubQuerier accepts the audit INSERT so LogAuditFull reaches dispatch.
type auditStubQuerier struct{}

func (auditStubQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (auditStubQuerier) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (auditStubQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func logOne(t *testing.T) {
	t.Helper()
	err := LogAuditFull(context.Background(), auditStubQuerier{},
		"user-1", "update", "invoice", "42", "invoice", nil, nil, nil, "", "", "")
	require.NoError(t, err)
}

func TestLogAuditFull_NotifiesEveryObserverWithActor(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	var got []AuditEvent
	RegisterAuditObserver(func(_ context.Context, _ Querier, ev AuditEvent) { got = append(got, ev) })
	RegisterAuditObserver(func(_ context.Context, _ Querier, ev AuditEvent) { got = append(got, ev) })

	logOne(t)

	want := AuditEvent{Action: "update", Resource: "invoice", ResourceID: "42", ActorUserID: "user-1"}
	assert.Equal(t, []AuditEvent{want, want}, got)
}

func TestLogAuditFull_NoObserversIsFine(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	logOne(t)
}

func TestLogAuditFull_ObserverPanicIsolated(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	called := false
	RegisterAuditObserver(func(context.Context, Querier, AuditEvent) { panic("boom") })
	RegisterAuditObserver(func(context.Context, Querier, AuditEvent) { called = true })

	assert.NotPanics(t, func() { logOne(t) })
	assert.True(t, called, "second observer must still run after the first panics")
}

func TestRegisterAuditObserver_IgnoresNil(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	RegisterAuditObserver(nil)
	logOne(t)
}
```

- [ ] **Step 3: Run it and verify it fails**

Run: `go test ./workflow/ -run 'TestLogAuditFull|TestRegisterAuditObserver' -v`
Expected: FAIL to compile (`undefined: AuditEvent`, `RegisterAuditObserver`, `resetAuditObservers`).

- [ ] **Step 4: Implement it.** Replace lines 10-27 of `workflow/audit.go` (the `AuditObserver` type, the `auditObserver` var and `SetAuditObserver`) with the code below, and add `"log/slog"` to the imports.

```go
// AuditEvent describes one audit row LogAuditFull wrote.
type AuditEvent struct {
	Action      string
	Resource    string
	ResourceID  string
	ActorUserID string // empty for system writes (e.g. the automation engine)
}

// AuditObserver is told about every audit row LogAuditFull writes, after the
// insert succeeded. It is how one write hook re-indexes every module for the
// AI assistant (and feeds the automation outbox) without each module's
// controller knowing about either. It must be best-effort: the primary
// operation has already committed. A panic is recovered and logged.
type AuditObserver func(ctx context.Context, q Querier, ev AuditEvent)

// auditObservers is copy-on-write so LogAuditFull never takes a lock.
var auditObservers atomic.Pointer[[]AuditObserver]

// RegisterAuditObserver adds an observer at boot, before the server takes
// traffic. It is process-wide because audit writes carry no
// dependency-injection seam. A nil observer is ignored.
func RegisterAuditObserver(o AuditObserver) {
	if o == nil {
		return
	}
	for {
		old := auditObservers.Load()
		var next []AuditObserver
		if old != nil {
			next = append(next, *old...)
		}
		next = append(next, o)
		if auditObservers.CompareAndSwap(old, &next) {
			return
		}
	}
}

// resetAuditObservers clears the registry; tests only.
func resetAuditObservers() { auditObservers.Store(nil) }

// notifyAuditObservers runs every observer, isolating panics so one
// misbehaving observer cannot starve the others or fail the write.
func notifyAuditObservers(ctx context.Context, q Querier, ev AuditEvent) {
	obs := auditObservers.Load()
	if obs == nil {
		return
	}
	for _, o := range *obs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("audit observer panicked", "panic", r, "resource", ev.Resource, "action", ev.Action)
				}
			}()
			o(ctx, q, ev)
		}()
	}
}
```

Then, at the end of `LogAuditFull`, replace

```go
	if o := auditObserver.Load(); o != nil {
		(*o)(ctx, q, action, resource, resourceID)
	}
```

with

```go
	notifyAuditObservers(ctx, q, AuditEvent{
		Action: action, Resource: resource, ResourceID: resourceID, ActorUserID: actorUserID,
	})
```

- [ ] **Step 5: Adapt the RAG observer.** In `rag_loader_router.go`, change the signature and bind the old names at the top of the body, so the rest of the function is unchanged:

```go
func ragAuditObserver(ctx context.Context, q workflow.Querier, ev workflow.AuditEvent) {
	action, resource, resourceID := ev.Action, ev.Resource, ev.ResourceID
```

In `main.go:279`, replace `workflow.SetAuditObserver(ragAuditObserver)` with `workflow.RegisterAuditObserver(ragAuditObserver)`.

- [ ] **Step 6: Run the tests and the build**

Run: `go build ./... && go test ./workflow/ -run 'TestLogAuditFull|TestRegisterAuditObserver' -v && go vet ./workflow/ .`
Expected: PASS, no vet findings. (If a `rag_*_test.go` calls `ragAuditObserver` with the old args, update the call to pass a `workflow.AuditEvent{...}`.)

- [ ] **Step 7: Commit**

```bash
git add workflow/audit.go workflow/audit_observer_test.go rag_loader_router.go main.go
git commit -m "refactor(workflow): multi-observer audit hook carrying actor id"
```

---

---

### Task 2: Tenant location, send window and the system-actor marker

**Files:**
- Modify: `automation/today.go`
- Create: `automation/window.go`, `automation/actor.go`
- Test: `automation/today_test.go` (append), `automation/window_test.go`, `automation/actor_test.go`

**Interfaces:**
- Produces:
  - `func TenantLocation(tzName string, fallback *time.Location) *time.Location`, with `TenantToday` reimplemented on top of it (behaviour unchanged, so existing tests stay green)
  - `func InSendWindow(now time.Time, loc *time.Location) bool`
  - `func NextSendWindow(now time.Time, loc *time.Location) time.Time`
  - `func WithSystemActor(ctx context.Context) context.Context`, `func IsSystemActor(ctx context.Context) bool`, `const SystemEmployeeID = 1`

- [ ] **Step 1: Write the failing tests.** Append to `automation/today_test.go`:

```go
func TestTenantLocation(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	tests := []struct {
		name, tz string
		fallback *time.Location
		want     string
	}{
		{"valid", "America/Denver", chicago, "America/Denver"},
		{"trimmed", "  America/Phoenix ", chicago, "America/Phoenix"},
		{"blank uses fallback", "", chicago, "America/Chicago"},
		{"garbage uses fallback", "Mars/Base", chicago, "America/Chicago"},
		{"nil fallback is UTC", "", nil, "UTC"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TenantLocation(tc.tz, tc.fallback).String())
		})
	}
}
```

Also add the fall-back night to `TestTenantToday`'s table:

```go
		{
			name: "DST fall-back, second 01:30 is still Nov 1",
			now:  time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC), // 01:30 CST (second pass)
			tz:   "America/Chicago", fallback: time.UTC, want: "2026-11-01",
		},
```

Create `automation/window_test.go`:

```go
package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextSendWindow(t *testing.T) {
	tests := []struct {
		name, tz, now, want string
	}{
		{"before window on spring-forward day", "America/Chicago", "2026-03-08T07:00:00Z", "2026-03-08T13:00:00Z"},
		{"second 01:30 on fall-back day", "America/Chicago", "2026-11-01T07:30:00Z", "2026-11-01T14:00:00Z"},
		{"inside window returns now", "America/Chicago", "2026-11-02T15:00:00Z", "2026-11-02T15:00:00Z"},
		{"18:00 is outside, next morning", "America/Chicago", "2026-11-02T00:00:00Z", "2026-11-02T14:00:00Z"},
		{"exactly 08:00 is inside", "America/New_York", "2026-07-01T12:00:00Z", "2026-07-01T12:00:00Z"},
		{"phoenix ignores DST", "America/Phoenix", "2026-03-08T13:00:00Z", "2026-03-08T15:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.tz)
			require.NoError(t, err)
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			want, err := time.Parse(time.RFC3339, tc.want)
			require.NoError(t, err)
			got := NextSendWindow(now, loc)
			assert.True(t, want.Equal(got), "got %s", got.UTC().Format(time.RFC3339))
			assert.True(t, InSendWindow(got, loc))
		})
	}
}
```

Create `automation/actor_test.go`:

```go
package automation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemActorMarker(t *testing.T) {
	ctx := context.Background()
	assert.False(t, IsSystemActor(ctx))
	assert.True(t, IsSystemActor(WithSystemActor(ctx)))
}
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./automation/ -run 'TenantLocation|SendWindow|SystemActor' -v`
Expected: FAIL to compile (undefined symbols).

- [ ] **Step 3: Implement it.** In `automation/today.go`, split the resolution out:

```go
// TenantLocation resolves a tenant's IANA timezone name, falling back to
// fallback (or UTC when nil) for a blank or unrecognised name.
func TenantLocation(tzName string, fallback *time.Location) *time.Location {
	loc := fallback
	if loc == nil {
		loc = time.UTC
	}
	if name := strings.TrimSpace(tzName); name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
		slog.Warn("automation: invalid tenant timezone, using fallback", "timezone", name, "fallback", loc.String())
	}
	return loc
}

// TenantToday returns the calendar date (midnight, in the resolved location)
// that it currently is for a tenant whose company profile names tzName.
func TenantToday(now time.Time, tzName string, fallback *time.Location) time.Time {
	loc := TenantLocation(tzName, fallback)
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
```

Create `automation/window.go`:

```go
package automation

import "time"

// The external send window, in tenant-local hours: [08:00, 18:00). Both ends
// are far from the 02:00 DST transitions, so a window edge always exists.
const (
	sendWindowStartHour = 8
	sendWindowEndHour   = 18
)

// InSendWindow reports whether now falls in [08:00, 18:00) in loc.
func InSendWindow(now time.Time, loc *time.Location) bool {
	h := now.In(loc).Hour()
	return h >= sendWindowStartHour && h < sendWindowEndHour
}

// NextSendWindow returns now when inside the window, otherwise the next 08:00
// instant in loc.
func NextSendWindow(now time.Time, loc *time.Location) time.Time {
	if InSendWindow(now, loc) {
		return now
	}
	l := now.In(loc)
	start := time.Date(l.Year(), l.Month(), l.Day(), sendWindowStartHour, 0, 0, 0, loc)
	if !l.Before(start) {
		start = time.Date(l.Year(), l.Month(), l.Day()+1, sendWindowStartHour, 0, 0, 0, loc)
	}
	return start
}
```

Create `automation/actor.go`:

```go
package automation

import "context"

// SystemEmployeeID is the tenant schema's seeded system employee
// (employee_id = 1, employee_is_system = TRUE). Employee-keyed columns written
// by automation use it; user-keyed actor fields stay empty.
const SystemEmployeeID = 1

type systemActorKey struct{}

// WithSystemActor marks ctx as running on behalf of the automation engine, so
// the automation audit observer ignores writes it caused (loop guard).
func WithSystemActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemActorKey{}, true)
}

// IsSystemActor reports whether ctx was marked by WithSystemActor.
func IsSystemActor(ctx context.Context) bool {
	v, _ := ctx.Value(systemActorKey{}).(bool)
	return v
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./automation/ -v`
Expected: PASS, including the untouched `TestTenantToday` and `runner_test.go`.

- [ ] **Step 5: Commit**

```bash
git add automation/
git commit -m "feat(automation): tenant location, send window and system-actor marker"
```

---

### Task 3: Automation tables in the tenant schema

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append at EOF)
- Test: `automation/schema_dbtest_test.go`

**Interfaces:**
- Produces tables: `automation_rules`, `automation_state`, `automation_deliveries`, `automation_outbox`, `automation_suppressions`, `automation_settings` (singleton row), with the column names below. P1 relies on these exact names.

- [ ] **Step 1: Load the `add-migration` skill** (Skill tool, `add-migration`) and follow its checklist for the edit below.

- [ ] **Step 2: Write the failing DB test** at `automation/schema_dbtest_test.go`

```go
//go:build dbtest

package automation

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTenantTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestAutomationSchema(t *testing.T) {
	ctx := context.Background()
	pool := newTenantTestPool(t)

	var ruleID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO automation_rules (rule_key, name, audience)
		VALUES ('test.schema', 'Schema test', 'internal')
		ON CONFLICT (rule_key) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`).Scan(&ruleID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1`, ruleID) })

	insert := `
		INSERT INTO automation_deliveries
			(rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind, run_at)
		VALUES ($1, 'invoice', '42', '2026-10-20', 0, $2, 'external', now())
		ON CONFLICT (occurrence_key) DO NOTHING`
	tag, err := pool.Exec(ctx, insert, ruleID, ruleID+":invoice:42:2026-10-20:0")
	require.NoError(t, err)
	assert.EqualValues(t, 1, tag.RowsAffected())
	tag, err = pool.Exec(ctx, insert, ruleID, ruleID+":invoice:42:2026-10-20:0")
	require.NoError(t, err)
	assert.EqualValues(t, 0, tag.RowsAffected(), "occurrence_key must dedupe")

	_, err = pool.Exec(ctx, `UPDATE automation_deliveries SET status = 'bogus' WHERE rule_id = $1`, ruleID)
	assert.Error(t, err, "status CHECK must reject unknown states")

	_, err = pool.Exec(ctx, `INSERT INTO automation_rules (rule_key, name, audience) VALUES ('test.bad', 'x', 'everyone')`)
	assert.Error(t, err, "audience CHECK must reject unknown audiences")

	_, err = pool.Exec(ctx, `INSERT INTO automation_suppressions (email, source) VALUES ('Mixed@Case.com', 'manual')`)
	assert.Error(t, err, "suppressed emails must be stored lower-cased")

	var cap int
	require.NoError(t, pool.QueryRow(ctx, `SELECT daily_external_cap FROM automation_settings`).Scan(&cap))
	assert.Equal(t, 200, cap)
}
```

- [ ] **Step 3: Run it and verify it fails** (against a fresh test DB with the current schema applied; see the memory note on schema verification: pgvector, `--single-transaction`, fresh DB)

Run: `go test -tags dbtest ./automation/ -run TestAutomationSchema -v`
Expected: FAIL (`relation "automation_rules" does not exist`), or SKIP if `TEST_DATABASE_URL` is unset. A skip is not a pass: set it before moving on.

- [ ] **Step 4: Append to the end of `database/migrations/tenant/schema.sql`**

```sql
-- ── Automation engine (docs/superpowers/specs/2026-10-14-automation-engine-design.md) ──
-- Rules, the per-occurrence delivery ledger (occurrence_key is the idempotency
-- primitive), an event outbox fed by the audit observer, an email suppression
-- list and tenant-level guard settings. All new tables: CHECKs live inside the
-- CREATE TABLE bodies, so no ADD CONSTRAINT guard is needed.
CREATE TABLE IF NOT EXISTS automation_rules (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_key    VARCHAR(100) NOT NULL UNIQUE,
    name        VARCHAR(200) NOT NULL,
    enabled     BOOLEAN      NOT NULL DEFAULT FALSE,
    is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    audience    VARCHAR(16)  NOT NULL,
    rule        JSONB        NOT NULL DEFAULT '{}',
    version     INT          NOT NULL DEFAULT 1,
    created_by  UUID,
    updated_by  UUID,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT automation_rules_audience_chk CHECK (audience IN ('internal', 'external'))
);

CREATE TABLE IF NOT EXISTS automation_state (
    rule_id         UUID        PRIMARY KEY REFERENCES automation_rules(id) ON DELETE CASCADE,
    enabled_at      TIMESTAMPTZ,
    last_planned_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS automation_deliveries (
    id                      UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id                 UUID         NOT NULL REFERENCES automation_rules(id) ON DELETE CASCADE,
    resource                VARCHAR(64)  NOT NULL,
    record_id               VARCHAR(64)  NOT NULL,
    anchor_date             DATE         NOT NULL,
    stage                   INT          NOT NULL,
    occurrence_key          VARCHAR(255) NOT NULL UNIQUE,
    recipient_kind          VARCHAR(16)  NOT NULL,
    recipient_email         VARCHAR(320) NOT NULL DEFAULT '',
    recipient_user_id       UUID,
    digest_key              VARCHAR(255),
    run_at                  TIMESTAMPTZ  NOT NULL,
    status                  VARCHAR(16)  NOT NULL DEFAULT 'planned',
    reason                  VARCHAR(32),
    attempts                INT          NOT NULL DEFAULT 0,
    lease_until             TIMESTAMPTZ,
    notify_notification_ids TEXT[]       NOT NULL DEFAULT '{}',
    last_error              TEXT,
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT automation_deliveries_status_chk
        CHECK (status IN ('planned', 'claimed', 'sent', 'skipped', 'failed', 'dead', 'unknown')),
    CONSTRAINT automation_deliveries_recipient_kind_chk
        CHECK (recipient_kind IN ('internal', 'external'))
);
CREATE INDEX IF NOT EXISTS idx_automation_deliveries_due    ON automation_deliveries (status, run_at);
CREATE INDEX IF NOT EXISTS idx_automation_deliveries_record ON automation_deliveries (resource, record_id);
CREATE INDEX IF NOT EXISTS idx_automation_deliveries_digest
    ON automation_deliveries (digest_key) WHERE digest_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS automation_outbox (
    id            BIGSERIAL   PRIMARY KEY,
    resource      VARCHAR(64) NOT NULL,
    record_id     VARCHAR(64) NOT NULL,
    action        VARCHAR(32) NOT NULL,
    actor_user_id UUID,
    depth         INT         NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_automation_outbox_pending
    ON automation_outbox (id) WHERE processed_at IS NULL;

CREATE TABLE IF NOT EXISTS automation_suppressions (
    email      VARCHAR(320) PRIMARY KEY,
    reason     VARCHAR(200) NOT NULL DEFAULT '',
    source     VARCHAR(16)  NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT automation_suppressions_lower_chk  CHECK (email = LOWER(email)),
    CONSTRAINT automation_suppressions_source_chk
        CHECK (source IN ('manual', 'bounce', 'complaint', 'unsubscribe'))
);

-- Singleton (id is always TRUE): per-tenant kill switch and send caps.
CREATE TABLE IF NOT EXISTS automation_settings (
    id                     BOOLEAN     PRIMARY KEY DEFAULT TRUE,
    kill_switch            BOOLEAN     NOT NULL DEFAULT FALSE,
    daily_external_cap     INT         NOT NULL DEFAULT 200,
    per_contact_weekly_cap INT         NOT NULL DEFAULT 3,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT automation_settings_singleton_chk CHECK (id)
);
INSERT INTO automation_settings (id) VALUES (TRUE) ON CONFLICT (id) DO NOTHING;
```

- [ ] **Step 5: Re-apply the schema twice to the test DB (idempotency), then run the test**

Run:
```bash
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction -f database/migrations/tenant/schema.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction -f database/migrations/tenant/schema.sql
go test -tags dbtest ./automation/ -run TestAutomationSchema -v
```
Expected: both applies succeed (the second is a no-op), and the test passes.

- [ ] **Step 6: Run the `migration-auditor` agent** on the schema diff. Fix anything it flags.

- [ ] **Step 7: Commit**

```bash
git add database/migrations/tenant/schema.sql automation/schema_dbtest_test.go
git commit -m "feat(schema): automation rules, delivery ledger, outbox, suppressions, settings"
```

---

---

### Task 4: `automation` RBAC resource

**Files:**
- Modify: `authz/catalog.go` (resource const after `ResourceAudit`; catalog rows before `{ResourceAudit, ActionRead}`)
- Test: `authz/automation_permission_test.go` (new)

**Interfaces:**
- Produces: `authz.ResourceAutomation = "automation"` with read/create/update/delete. P1 and P2 controllers `Require` it.

- [ ] **Step 1: Write the failing test** at `authz/automation_permission_test.go`

```go
package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAutomationPermissionsInCatalog(t *testing.T) {
	for _, a := range []Action{ActionRead, ActionCreate, ActionUpdate, ActionDelete} {
		assert.True(t, IsValidPermission(ResourceAutomation, a), "automation:%s must be grantable", a)
	}
	assert.False(t, IsValidPermission(ResourceAutomation, ActionApprove))
}
```

- [ ] **Step 2: Run it and verify it fails**

Run: `go test ./authz/ -run TestAutomationPermissionsInCatalog -v`
Expected: FAIL to compile (undefined `ResourceAutomation`).

- [ ] **Step 3: Implement it.** In `authz/catalog.go`, add after `ResourceAudit`:

```go
	// ResourceAutomation governs automation rules (reminders, escalations),
	// their delivery log and the tenant kill switch.
	ResourceAutomation Resource = "automation"
```

and add before `{ResourceAudit, ActionRead},` in the catalog slice:

```go
	{ResourceAutomation, ActionRead},
	{ResourceAutomation, ActionCreate},
	{ResourceAutomation, ActionUpdate},
	{ResourceAutomation, ActionDelete},
```

super_admin holds the `*` wildcard, so it gets these automatically. There's no role backfill, because no existing role should silently gain automation rights. If a frontend permission matrix or seed lists resources explicitly (`grep -rn "ResourceAudit" --include='*.go' . | grep -v _test`), add `automation` next to `audit` there too.

- [ ] **Step 4: Run the tests**

Run: `go test ./authz/ ./controllers/ -v -run 'Automation|Catalog|Permission'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add authz/
git commit -m "feat(authz): automation RBAC resource"
```

---

### Task 5: Notify idempotency key (stonesuite-notify + backend client)

**Files (repo `../stonesuite-notify`, on its own branch `feat/idempotency-key`):**
- Modify: `database/migrations/schema.sql` (append)
- Modify: `notifications/types.go` (`CreateInput` gains `IdempotencyKey string`)
- Modify: `notifications/store.go` (`Create` becomes idempotent when a key is set)
- Modify: `controllers/notifications.go` (`createNotificationRequest.IdempotencyKey`; per-recipient key; skip delivery enqueue on replay)
- Test: `notifications/store_dbtest_test.go` (new, `//go:build dbtest`), plus `controllers/notifications_test.go`

**Files (this repo):**
- Modify: `services/notify.go` (`NotificationRequest.IdempotencyKey string \`json:"idempotencyKey,omitempty"\``)
- Test: `services/notify_test.go` (append)

**Interfaces:**
- Produces: `NotificationRequest.IdempotencyKey`. When set, a replayed request returns the same notification ids and sends nothing again. When empty, behaviour is unchanged.

- [ ] **Step 1: Read notify's conventions first.** Read `../stonesuite-notify/CLAUDE.md` and `AGENTS.md`, and find how its store tests connect (`grep -rn "TEST_DATABASE_URL" ../stonesuite-notify --include='*_test.go' | head`). Follow that repo's rules where they differ from this plan.

- [ ] **Step 2: Write the failing store test.** Notify has no DB-backed store tests yet (only `notifications/types_test.go`), so this creates the harness. Create `../stonesuite-notify/notifications/store_dbtest_test.go`:

```go
//go:build dbtest

package notifications_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"<notify module>/database"
	"<notify module>/notifications"
)

// Fixed UUIDs: tenant_id, recipient_user_id and resource_id are UUID NOT NULL.
const (
	testTenantID   = "00000000-0000-4000-8000-000000000001"
	testUserID     = "00000000-0000-4000-8000-000000000002"
	testResourceID = "00000000-0000-4000-8000-000000000003"
)

// newTestStore connects to TEST_DATABASE_URL (skipping when unset), applies the
// embedded schema and returns a PGStore on it.
func newTestStore(t *testing.T) (*notifications.PGStore, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, database.ApplySchema(ctx, pool))
	_, err = pool.Exec(ctx, `DELETE FROM notifications WHERE tenant_id = $1`, testTenantID)
	require.NoError(t, err)
	return notifications.NewPGStore(pool), pool
}

func TestCreateIdempotent_ReplayReturnsSameRow(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	in := notifications.CreateInput{
		TenantID: testTenantID, RecipientUserID: testUserID, RecipientEmail: "a@b.com",
		EventType: "automation.reminder", Resource: "invoice", ResourceID: testResourceID,
		Title: "Invoice due", IdempotencyKey: "rule:invoice:42:2026-10-20:0:" + testUserID + ":a@b.com",
	}
	first, created1, err := store.CreateIdempotent(ctx, in)
	require.NoError(t, err)
	assert.True(t, created1)
	second, created2, err := store.CreateIdempotent(ctx, in)
	require.NoError(t, err)
	assert.False(t, created2, "replay must not create a second row")
	assert.Equal(t, first.ID, second.ID)

	in.IdempotencyKey = ""
	a, _, err := store.CreateIdempotent(ctx, in)
	require.NoError(t, err)
	b, _, err := store.CreateIdempotent(ctx, in)
	require.NoError(t, err)
	assert.NotEqual(t, a.ID, b.ID, "no key keeps today's behaviour")
}
```

Replace `<notify module>` with the module path in `../stonesuite-notify/go.mod`, and match `NewPGStore`'s real constructor name and signature (`grep -n "func NewPGStore" ../stonesuite-notify/notifications/store.go`). The `DELETE` is a test-fixture reset scoped to the fixed test tenant only. Point `TEST_DATABASE_URL` at a scratch database, never a shared one.

- [ ] **Step 3: Run it and verify it fails**

Run (in `../stonesuite-notify`): `go test -tags dbtest ./notifications/ -run TestCreate_IdempotentReplay -v` (use the tag the repo uses)
Expected: FAIL (`CreateIdempotent` undefined).

- [ ] **Step 4: Implement it in notify.** Append to `database/migrations/schema.sql`:

```sql
-- Idempotency for callers that may retry after a timeout (automation engine):
-- the same (tenant, key) never creates a second notification or delivery.
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(255);
CREATE UNIQUE INDEX IF NOT EXISTS uq_notifications_tenant_idempotency
    ON notifications (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
```

Add `IdempotencyKey string \`json:"idempotencyKey,omitempty"\`` to `CreateInput` and `CreateIdempotent(ctx, in CreateInput) (*Notification, bool, error)` to the `Store` interface. Add to `notifications/store.go`:

```go
// CreateIdempotent inserts like Create, but when in.IdempotencyKey is set and a
// row with the same (tenant, key) exists it returns that row with created=false.
func (s *PGStore) CreateIdempotent(ctx context.Context, in CreateInput) (*Notification, bool, error) {
	if in.IdempotencyKey == "" {
		n, err := s.Create(ctx, in)
		return n, err == nil, err
	}
	var actorUserIDArg any
	if in.ActorUserID != "" {
		actorUserIDArg = in.ActorUserID
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO notifications
			(tenant_id, recipient_user_id, recipient_email, actor_user_id, event_type, resource, resource_id, title, body, link, visible_in_app, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING `+notificationColumns,
		in.TenantID, in.RecipientUserID, in.RecipientEmail, actorUserIDArg, in.EventType, in.Resource, in.ResourceID, in.Title, in.Body, in.Link, in.VisibleInApp, in.IdempotencyKey)
	n, err := scanNotification(row)
	if err == nil {
		return n, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("create notification (idempotent): %w", err)
	}
	existing, err := scanNotification(s.pool.QueryRow(ctx,
		`SELECT `+notificationColumns+` FROM notifications WHERE tenant_id = $1 AND idempotency_key = $2`,
		in.TenantID, in.IdempotencyKey))
	if err != nil {
		return nil, false, fmt.Errorf("load idempotent notification: %w", err)
	}
	return existing, false, nil
}
```

Update every other `Store` implementation to satisfy the interface. Today that is `fakeStore` in `controllers/notifications_test.go:31`: give it a `CreateIdempotent` that keeps a `map[string]*Notification` by key, plus an enqueue counter for the controller test. In `controllers/notifications.go`, add `IdempotencyKey string \`json:"idempotencyKey,omitempty"\`` to `createNotificationRequest`. In the planning loop, set:

```go
		if req.IdempotencyKey != "" {
			in.IdempotencyKey = req.IdempotencyKey + ":" + recipient.UserID + ":" + strings.ToLower(recipient.Email)
		}
```

In the create loop, replace `h.Store.Create(...)` with:

```go
		n, isNew, err := h.Store.CreateIdempotent(r.Context(), p.input)
		if err != nil {
			log.Printf("notifications: create for recipient %s: %v", p.input.RecipientUserID, err)
			continue
		}
		created = append(created, n)
		if !isNew {
			continue // replay: deliveries were enqueued by the original request
		}
		h.enqueueDeliveries(r.Context(), *n, p.prefs, wantsEmail)
```

(Keep the audit-record call inside the `isNew` path.) Add a controller test in that repo's existing handler-test style: POST the same body with `idempotencyKey` twice, and assert the same ids come back and the delivery enqueue count stays at 1.

- [ ] **Step 5: Run notify's tests**

Run (in `../stonesuite-notify`): `go build ./... && go test ./... && go test -tags dbtest ./notifications/ -run Idempotent -v`
Expected: PASS.

- [ ] **Step 6: Commit in notify**

```bash
cd ../stonesuite-notify && git checkout -b feat/idempotency-key
git add database/migrations/schema.sql notifications/ controllers/
git commit -m "feat: idempotency key on internal notification create"
cd -
```

- [ ] **Step 7: Backend client field, test first.** Append to `services/notify_test.go`:

```go
func TestNotificationRequest_IdempotencyKeyJSON(t *testing.T) {
	b, err := json.Marshal(NotificationRequest{TenantID: "t", IdempotencyKey: "rule:invoice:42:2026-10-20:0"})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"idempotencyKey":"rule:invoice:42:2026-10-20:0"`)

	b, err = json.Marshal(NotificationRequest{TenantID: "t"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "idempotencyKey", "omitted when empty so existing calls are unchanged")
}
```

Run: `go test ./services/ -run TestNotificationRequest_IdempotencyKeyJSON -v`, which is expected to FAIL to compile. Then add to `NotificationRequest` in `services/notify.go`, after `ActorUserID`:

```go
	// IdempotencyKey makes a retried create safe: notify returns the original
	// notifications instead of sending again. Set it whenever a caller may
	// retry after a timeout (the automation engine always does).
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
```

Confirm that `notifyPayload` embeds `NotificationRequest`, so the field serialises (`grep -n "type notifyPayload" -A4 services/notify.go`). Run the test again: PASS.

- [ ] **Step 8: Commit in the backend**

```bash
git add services/notify.go services/notify_test.go
git commit -m "feat(services): idempotency key on notify requests"
```

**Deploy order:** notify first (it ignores the unknown field until then, so either order is safe, but notify-first means P1 gets real dedupe from day one).

---

---

### Task 6: Project docs and final verification

**Files:**
- Modify: `CLAUDE.md` (add an "Automation Engine" section under Strict Implementation Rules, after Global Search; add `automation/` and `automation-cron/` to the Repo Structure tree if #361 didn't)

- [ ] **Step 1: Add to `CLAUDE.md`:**

```markdown
### Automation Engine (`automation/` + `POST /api/internal/automation/tick`)
The `automation-cron` Worker signs a tick every 15 min; `automation.Runner` runs every
`DefaultJobs()` entry per servable tenant **inside the request** (scale-to-zero can stop the
machine after the response).
1. **"Today" is tenant-local, computed in Go** (`automation.TenantToday` /
   `TenantLocation`, from `company_profile.timezone`, fallback
   `AUTOMATION_DEFAULT_TIMEZONE`) and passed as a `$n` param. Automation SQL never uses
   `CURRENT_DATE`. External sends respect `InSendWindow` (08:00–18:00 local).
2. **Writes made by automation run under `automation.WithSystemActor(ctx)`**, so audit
   observers ignore them (loop guard). Employee-keyed columns use
   `automation.SystemEmployeeID` (seeded employee 1). There is no automation `users` row;
   don't add one.
3. **Every outbound send carries `services.NotificationRequest.IdempotencyKey`** (the
   delivery's occurrence key), so a retry after a timeout can't double-send.
4. **Audit observers are a list** (`workflow.RegisterAuditObserver`). Each one runs
   panic-isolated, and none may block the write.
```

- [ ] **Step 2: Full verification**

```bash
go build ./... && go vet ./... && go test -race ./...
go test -tags dbtest ./automation/ ./workflow/ -v
```
Expected: everything passes. The dbtest run must not report SKIP for the automation schema test; if it does, set `TEST_DATABASE_URL` to a fresh database with pgvector (see the schema-verification memory).

- [ ] **Step 3: Run the review agents** on the branch diff: `migration-auditor` (schema) and `tenancy-security-reviewer` (audit hook, RBAC). Fix any confirmed finding and re-run Step 2.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: automation engine rules in CLAUDE.md"
```

- [ ] **Step 5: Rollout (manual, needs the user)**
1. Deploy notify with the idempotency change (it's backward compatible; an absent key behaves as today).
2. Merge the backend. The new tenant tables apply on the next boot of every tenant.
3. The #361 rollout (setting `AUTOMATION_TICK_SECRET` and deploying `automation-cron` per environment) is independent and still pending.

# Dashboard Widgets — Backend Design Spec

**Date:** 2026-08-06
**Status:** Approved by user in planning session, proceeding to implementation. Branch `feat/dashboard-widgets`.
**Scope:** Backend support for the Dashboard Widgets module — a code-defined catalog of dashboard widgets, filtered per caller by RBAC and tenant configuration, plus per-user visibility/layout preferences. The frontend `DashboardPage.tsx` (`StoneSuite-WebUI`) is currently a placeholder with no widget grid yet; this spec ships the API it will consume, modeled on the one client-side precedent that already does permission-gated, declarative UI composition: `src/config/sidebarNav.ts` + `useUserPermissions`.

---

## 1. Overview & Goals

Add a **Dashboard Widgets** module: `GET /api/tenant/dashboard/widgets` returns the subset of a fixed widget catalog the calling user is authorized to see (RBAC) and the tenant has not disabled (tenant configuration), overlaid with that user's own saved visibility/position/size. `PUT`/`DELETE` on `.../preferences` let a user save or reset their own layout. A small admin surface (`GET`/`PUT /api/tenant/dashboard/config`, gated by `workflow_config:configure`) lets a super admin turn individual widgets off tenant-wide.

This follows the same shape as `sidebarNav.ts` on the frontend — a static, code-defined list of entries each declaring the `{resource, action}` grant it needs — moved server-side so the filtering can't be bypassed by a client that already has the JSON. It reuses the existing RBAC enforcer (`authz.EffectiveGrants` / `authz.DecideAny`) exactly as written; no new authorization primitive is introduced.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Effective grants for the caller | `authz.EffectiveGrants` | `authz/store.go` |
| Pure grant-decision (no DB) for a resource/action pair | `authz.DecideAny` | `authz/enforcer.go` |
| Identity → tenant `users.id` | `workflow.UserIDByIdentity` | `workflow/store.go:483` |
| `{success, message}` response envelope | `writeJSON` / `fail` | `controllers/tenant.go` |
| Security-event logging | `logSecurityEvent` | `controllers/security_log.go` |
| Admin-config gate precedent (`workflow_config:configure`) | `CRMAdminOps.requireConfig` | `controllers/crm_admin.go` |
| Natural-key, no-FK config-table precedent (code catalog, not a DB-seeded table) | `workflow_numbering_configs` | `tenant/schema.sql:466` |
| Per-user composite-PK table precedent | `user_roles` | `tenant/schema.sql:76` |
| Client-side precedent for the exact same idea (declarative, permission-gated nav) | `sidebarNav.ts` + `useUserPermissions` | `StoneSuite-WebUI/src/config/sidebarNav.ts` |

### What is genuinely missing (new tables — justified below)

- `dashboard_widget_config` — no table represents a tenant-wide widget on/off switch.
- `dashboard_user_widget` — no table represents a user's personal dashboard layout.

---

## 2. Architecture Decisions

**AD-1 — The widget catalog is Go code, not a database table.** Mirrors `authz/catalog.go` and `sidebarNav.ts`: adding a widget is a one-line append to `dashboard/catalog.go`, requires no migration, and cannot drift between "what exists" and "what schema.sql seeded" the way a DB-seeded catalog could. `dashboard_widget_config` stores **override rows only** — a tenant that has never touched widget settings has zero rows in it, and every catalog widget defaults to enabled. This is the same posture `workflow_numbering_configs` takes toward `workflows` (lazy upsert, no seeding).

**AD-2 — Each widget rides on an existing `authz` permission; no new `ResourceDashboard` is introduced.** A widget's visibility gate *is* the `{resource, action}` of the data it summarizes (e.g. the Quotes widget requires `quote:read`) — inventing a separate `dashboard:read` permission would add a second, redundant gate that could drift from the real one (a role could grant `dashboard:read` without `quote:read` and the widget would show a quote count the role can't otherwise see). The base `GET /widgets` endpoint itself requires only authentication, exactly like `GET /users/me/permissions` — filtering happens per-widget, not at the route.
The two **admin config** routes (`GET`/`PUT /dashboard/config`) are the exception: they reuse `ResourceWorkflowConfig`/`ActionConfigure`, the existing catalog-wide "configure this workspace" gate `SetDesignVersion` and `SetWorkflowApprovers` already use.

**AD-3 — Widget descriptors only; no aggregate values.** `GET /widgets` returns metadata (title, category, type, `dataEndpoint`) — never a computed count/sum. The frontend renders each widget by calling the `dataEndpoint` it already has a service for (`quoteService`, `invoiceService`, …). This keeps the module a thin authorization + preferences layer with no new scope-filtered aggregate-query surface, and it means a widget's number is never stale relative to the page the user would navigate to for the full list.

**AD-4 — Authorized-but-hidden widgets are still returned, with `visible:false`.** Only two things ever remove a widget from the response entirely: the caller lacking the grant, or the tenant disabling it. A widget the *user* has toggled off stays in the payload so a "manage widgets" panel can list everything the user is allowed to add back — the alternative (omitting hidden widgets) would make them unrecoverable without a hard reset. `DELETE .../preferences` is that hard reset: it clears all of the caller's saved rows, reverting every widget to its catalog default.

**AD-5 — Preference writes are validated against both the catalog and the caller's live grants**, not just the catalog. `PUT .../preferences` rejects (400) any `widgetKey` that doesn't exist, and separately rejects (400 + `logSecurityEvent`) any key the caller does not currently hold the grant for — a caller cannot persist state for a widget it cannot see, which matters because a role's grants can shrink after a preference was saved under a broader role, and because this is the one write path in the module, so it's the one place that needs to defend against a crafted request rather than trust the client's last-rendered widget list.

**AD-6 — Layout bounds are fixed, generic grid units, not tied to a specific frontend grid library.** `StoneSuite-WebUI` has no grid/drag-drop dependency yet (`DashboardPage.tsx` is a placeholder) — checked `package.json`, no `react-grid-layout`/`gridstack`/`dnd-kit`. Rather than guess a library and bake its coordinate system into the API, `width`/`height` are plain integers on a conventional 12-column-by-8-row grid (`MinSize=1`, `MaxWidth=12`, `MaxHeight=8`), the same convention `react-grid-layout` and Bootstrap both use — whatever grid component the frontend eventually adopts can map onto it directly, and the bounds exist purely so a malformed request can't persist a nonsensical layout (e.g. `width: -4`).

**AD-7 — `position` is a single integer sort key, not `(x, y)` coordinates.** A full 2-D placement model is a frontend layout-engine concern; the backend's job is "resolve which widgets, in what order, at what size" and hand that to whatever grid renderer the frontend builds. `position` (ascending) plus `width`/`height` is enough for a reasonable default grid flow and is trivially forward-compatible — if the frontend later wants true `(x, y)`, that's an additive column, not a breaking change.

**AD-8 — Tenant-disabled beats every role, including `super_admin`.** `Resolve` checks the tenant override before the grant, and the override short-circuits regardless of scope. A widget the workspace has turned off is off for everyone; this is a workspace setting, not a permission.

---

## 3. Schema

Both appended to `database/migrations/tenant/schema.sql`, after `workflow_numbering_configs` (the closest existing precedent — natural-key config table, no seed rows).

```sql
-- dashboard_widget_config -- tenant-wide widget on/off override (AD-1, AD-8) ------------
-- Override-only: a widget with no row here is enabled. widget_key is not a real FK
-- (the catalog is Go code, not a table -- same non-FK pattern as role_permissions.resource).
CREATE TABLE IF NOT EXISTS dashboard_widget_config (
    widget_key  VARCHAR(64) PRIMARY KEY,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- dashboard_user_widget -- one caller's visibility/layout for one widget (AD-4, AD-6, AD-7)
CREATE TABLE IF NOT EXISTS dashboard_user_widget (
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    widget_key  VARCHAR(64) NOT NULL,
    visible     BOOLEAN     NOT NULL DEFAULT TRUE,
    position    INT         NOT NULL DEFAULT 0,
    width       INT         NOT NULL DEFAULT 4,
    height      INT         NOT NULL DEFAULT 2,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, widget_key),
    CONSTRAINT chk_dashboard_user_widget_position CHECK (position >= 0),
    CONSTRAINT chk_dashboard_user_widget_size CHECK (width BETWEEN 1 AND 12 AND height BETWEEN 1 AND 8)
);
CREATE INDEX IF NOT EXISTS idx_dashboard_user_widget_user ON dashboard_user_widget(user_id);
```

No seed data — the catalog itself lives in Go (AD-1).

---

## 4. Package Layout

```
dashboard/                    (new)
  catalog.go                  Widget struct, WidgetType/Category consts, the catalog
                               slice (~20 entries), Catalog(), ByKey()
  catalog_test.go              unique keys, defaults in bounds, every {Resource,Action}
                               present in authz.Catalog() (drift guard, mirrors
                               rbac_catalog_drift_test.go)
  types.go                     UserPref, ResolvedWidget, PrefInput, ConfigEntry,
                               ForbiddenWidgetError, ClientError
  resolve.go + _test.go        Resolve(catalog, grants, overrides, prefs) -- pure
  validate.go + _test.go       ValidatePrefs(inputs, grants), ValidateConfigUpdates(inputs) -- pure
  store.go                     WidgetConfigOverrides, SetWidgetConfig, UserPrefs,
                               SaveUserPrefs, ClearUserPrefs

controllers/
  dashboard.go                 DashboardOps: ListWidgets, SavePreferences,
                               ResetPreferences, GetConfig, SetConfig
  dashboard_test.go             401/403/400 table-driven handler tests
```

Every file stays under the 300-line cap; `dashboard/` has no dependency on `controllers/` (handlers depend on the package, never the reverse), matching the rest of the codebase.

---

## 5. API Surface

All routes under `tenantChain` (`RequireAuth` → per-tenant rate limit → `TenantResolver`), registered in `main.go` next to `GET /api/tenant/users/me/permissions` — both are "what can I, the caller, see" endpoints.

| Method | Route | Permission | Notes |
|---|---|---|---|
| `GET` | `/dashboard/widgets` | authenticated only; per-widget grant (AD-2) | catalog ∩ grants ∩ tenant config, overlaid with caller's prefs |
| `PUT` | `/dashboard/widgets/preferences` | authenticated; per-widget grant re-checked (AD-5) | bulk upsert of the caller's own visibility/position/size; returns the refreshed list |
| `DELETE` | `/dashboard/widgets/preferences` | authenticated | clears all of the caller's saved prefs; returns the refreshed (default) list |
| `GET` | `/dashboard/config` | `workflow_config:configure` | every catalog widget + its tenant-effective enabled flag |
| `PUT` | `/dashboard/config` | `workflow_config:configure` | bulk upsert of tenant-wide enable/disable overrides |

**Wiring edits (additive, isolated):**
- `main.go`: one constructor (`dashboardOps := controllers.NewDashboardOps()`) + 5 `mux.Handle` lines, all `tenantChain`.
- No changes to `authz/catalog.go` — every widget reuses an existing `{Resource, Action}` (AD-2).

### Response shapes

```jsonc
// GET /dashboard/widgets
{
  "success": true,
  "widgets": [
    {
      "key": "sales.quotes", "title": "Quotes", "description": "...",
      "category": "sales", "type": "list", "dataEndpoint": "/api/tenant/quotes",
      "scope": "own",        // caller's granted scope for this widget's resource ("all"|"own")
      "visible": true, "position": 3, "width": 6, "height": 4
    }
  ]
}

// PUT /dashboard/widgets/preferences  body
{ "widgets": [ { "widgetKey": "sales.quotes", "visible": false, "position": 0, "width": 4, "height": 2 } ] }

// GET /dashboard/config
{ "success": true, "widgets": [ { "key": "sales.quotes", "title": "Quotes", "category": "sales", "enabled": true } ] }

// PUT /dashboard/config  body
{ "widgets": [ { "widgetKey": "sales.quotes", "enabled": false } ] }
```

---

## 6. Validation & Error Handling

| Validation | Enforced by |
|---|---|
| `widgetKey` exists in catalog | `ValidatePrefs` / `ValidateConfigUpdates` → `400` |
| Caller currently holds the widget's grant | `ValidatePrefs` via `authz.DecideAny` → `400` + `logSecurityEvent("dashboard_pref_denied", ...)` (AD-5) |
| `position >= 0`, `width`/`height` in `[1,12]`/`[1,8]` | `ValidatePrefs` (Go) + `CHECK` constraints (DB, defense in depth) → `400` |
| Tenant-disabled widget never returned, any role | `Resolve` checks overrides before grants (AD-8) |

### Status codes

| Code | Cause |
|---|---|
| `400` | unknown/unauthorized `widgetKey`; out-of-bounds `position`/`width`/`height`; malformed body |
| `401` | no authenticated caller |
| `403` | `workflow_config:configure` denied on the two config routes |
| `500` | pool/grant/store errors only |

`GET /dashboard/widgets` never 403s for an ordinary caller — a zero-grant guest gets `200` with an empty (or near-empty) list, same posture as `MyPermissions`.

---

## 7. Testing

**Pure functions — stdlib `testing`, table-driven:**

- `dashboard/resolve_test.go`: wildcard `super_admin` grant sees every enabled widget; zero-grant caller sees none; tenant-disabled hides a widget even from a wildcard grant (AD-8); a saved pref for a key no longer in the catalog is ignored; missing prefs fall back to catalog defaults; output sorted by `position`.
- `dashboard/validate_test.go`: unknown key → `ClientError`; grant-missing key → `ForbiddenWidgetError`; `position < 0` / `width`/`height` out of `[1,12]`/`[1,8]` → `ClientError`; happy path returns the expected `UserPref` slice.
- `dashboard/catalog_test.go`: every key unique; every `{Resource, Action}` present in `authz.Catalog()` (`authz.IsValidPermission`) — an ungrantable widget is a bug, same class of drift `rbac_catalog_drift_test.go` guards for CRM resources; every `DefaultWidth`/`DefaultHeight` within the AD-6 bounds.

**Handler tests (`controllers/dashboard_test.go`):** no-auth → `401`; config routes without `workflow_config:configure` → `403`; `PUT preferences` with an unauthorized `widgetKey` → `400` (and confirms the security-log call fires, mirroring the pattern in `scope_test.go`); happy-path round trip.

**No `dbtest` suite is required** — `store.go`'s queries are simple single-table upserts/selects with no invariant beyond what the `CHECK` constraints already enforce at the DB layer and the `400` validation already enforces in Go; the existing `-tags dbtest` convention is reserved for stateful multi-step invariants (posting, balances, concurrent locks) that this module doesn't have.

`go build ./... && go vet ./... && go test ./...` must pass; run `tenancy-security-reviewer` after the handlers land (new tenant-scoped handlers + store queries, its stated trigger).

---

## 8. Out of Scope

- **Aggregate metric values** (open-quote counts, AR totals, low-stock counts). AD-3 — the frontend computes/fetches these from the existing module endpoints.
- **True 2-D `(x, y)` drag-and-drop layout.** AD-7 — `position` + `width`/`height` only; the frontend has no grid library yet to target.
- **Custom/user-authored widgets.** The catalog is fixed, code-defined content (AD-1) — not a widget builder.
- **Multiple named dashboards / per-role default layouts.** One layout per user, full stop; a tenant-wide default layout is a `DELETE`-then-inherit-catalog-defaults away from being unnecessary.
- **Real-time push updates to widget data.** Each widget's `dataEndpoint` is polled/fetched by the frontend exactly like every other list page today; no websocket/SSE channel is introduced.

# Direct Cross-Origin API Calls for the Azure DevOps Dev Environment

**Status:** Approved, not yet implemented
**Date:** 2026-08-12
**Repos affected:** `StoneSuite-Backend` (primary), `StoneSuite-WebUI`

## Context

The Azure DevOps dev deployment (`dev-stonesuite-webui.pages.dev` →
`dev-stonesuite-api.fly.dev`) currently calls the backend through a
same-origin Cloudflare Pages Function proxy (`functions/api/[[path]].ts`),
which exists so the backend's `SameSite=Lax` `auth_token` cookie survives —
a cross-origin response can't set a Lax cookie that the browser will send
back on a later cross-site request.

The user wants the Azure dev frontend to call the backend directly via an
absolute cross-origin URL instead — the more commonly used client/server
API shape, and the one already used by `stonesuite.pages.dev`/
`dev.stonesuite.app` (GitHub Actions deploys).

**Important finding surfaced during this design:** the GitHub-Actions-deployed
`dev.stonesuite.app` already calls the backend directly today
(`https://stonesuite-backend.fly.dev/api/auth/tenant-login`, confirmed via a
live network capture), but its cookies are still `SameSite=Lax`. This is the
exact failure mode `DEPLOY_FRONTEND.md` warns about: the cookie is stored but
never re-sent on a cross-site request, so `refresh_token`-based silent
refresh silently fails and users are logged out once their 1-hour access
token expires. It also has no separate dev backend — it hits
`stonesuite-backend.fly.dev`, the real production backend.

**Decision:** that bug is out of scope for this change (it lives on the prod
backend, and fixing it there is a separate, higher-stakes piece of work).
It's noted here as a known issue to schedule separately — see
[Known issue: dev.stonesuite.app](#known-issue-devstonesuiteapp-not-in-scope) below.

## Scope

**Dev only.** `dev-stonesuite-webui.pages.dev` → `dev-stonesuite-api.fly.dev`
directly, cross-origin, with `SameSite=None` + CSRF protection.

**Prod is untouched.** `stonesuite.pages.dev` → `stonesuite-backend.fly.dev`
keeps the current same-origin proxy design, `SameSite=Lax`, no CSRF token.
No prod code path changes behavior as a result of this work.

## Why dev and prod need to diverge safely

Both the Azure (dev) and GitHub Actions (prod) build/deploy pipelines build
from the same `master` branch — same binary, same commit, different target
apps. A cookie/CORS code change can't be "dev only" by branch alone; it needs
a runtime toggle so the same binary behaves differently on
`dev-stonesuite-api` vs. `stonesuite-backend`.

## Design

### Backend (`StoneSuite-Backend`)

**New config field**, following the existing `CorsOrigin`/`FRONTEND_URL`
pattern (`config/config.go`):

```go
// CookieSameSite controls the SameSite attribute on auth cookies. "lax"
// (default) for same-origin-proxied deployments; "none" for deployments
// where the frontend calls this backend cross-origin directly. "none"
// requires the CSRF double-submit-cookie middleware to be active — see
// middleware/csrf.go.
CookieSameSite string
```
Env var: `COOKIE_SAME_SITE_MODE`, default `"lax"`.

- `dev-stonesuite-api`'s `fly.dev.toml`: add `COOKIE_SAME_SITE_MODE = "none"`.
- `stonesuite-backend`'s `fly.toml`: no change (stays on the `"lax"` default).

**Prerequisite fix, discovered during this design:** all 4 cookie call sites
set `Secure: config.AppConfig.IsProduction()`, which is `APP_ENV ==
"production"`. Neither `fly.toml` nor `fly.dev.toml` sets `APP_ENV` — the
Set-Cookie headers in both network captures above confirm this (no `Secure`
attribute present on either). Browsers **reject `SameSite=None` cookies
outright if `Secure` is not also set**, so without a fix, `auth_token`,
`refresh_token`, and `csrf_token` would silently fail to be stored on
`dev-stonesuite-api` the moment `COOKIE_SAME_SITE_MODE` flips to `"none"`.

Fix: add `APP_ENV = "production"` to `dev-stonesuite-api`'s `fly.dev.toml`
`[env]` block. This is also just correct on its own merits — the app is only
ever served over HTTPS on Fly, so there's no scenario where a non-`Secure`
cookie is appropriate there. `stonesuite-backend`'s `fly.toml` is
intentionally left as-is (out of scope — see the known issue below, which
already covers the same underlying gap on the prod backend).

All 4 `http.SetCookie` call sites read this instead of hardcoding
`http.SameSiteLaxMode`:
- `controllers/tenant.go` — `TenantLogin` (~line 691) and `RefreshToken`
  (~line 872)
- `controllers/rbac.go` — role switch (~line 374)
- `controllers/saml_exchange.go` — SSO exchange (~line 82)

**New CSRF middleware** (`middleware/csrf.go`), a no-op when
`CookieSameSite != "none"` (so prod behavior is provably unchanged):

- The same 4 call sites above also issue a `csrf_token` cookie: random
  value, **not** `HttpOnly` (must be JS-readable), same `Secure`/`SameSite`
  as the auth cookie.
- Middleware wraps all state-changing routes (`POST`/`PUT`/`PATCH`/`DELETE`):
  requires the `X-CSRF-Token` request header to exactly match the
  `csrf_token` cookie value. Mismatch or missing header → `403` +
  `logSecurityEvent(r, "csrf_mismatch", ...)` per the existing security
  event logging convention.
- `main.go`'s CORS handler: add `X-CSRF-Token` to
  `Access-Control-Allow-Headers`.

### Frontend (`StoneSuite-WebUI`)

- Azure pipeline 6's `VITE_API_BASE_URL` changes from `/api` to
  `https://dev-stonesuite-api.fly.dev/api`. **GitHub Actions' variable is
  untouched.**
- `src/api/client.ts`: read the `csrf_token` cookie and attach it as
  `X-CSRF-Token` in the existing request interceptor (same place the
  `Authorization` bearer header is already attached).
- `functions/api/[[path]].ts` stays in the repo unchanged (prod still needs
  it) — it just goes unused for dev's own traffic once dev calls the backend
  directly.

### Deployment

- `dev-stonesuite-api`'s `fly.dev.toml`: add `COOKIE_SAME_SITE_MODE = "none"`.
  `CORS_ORIGIN` already correctly includes `https://dev-stonesuite-webui.pages.dev`
  — no change needed there.
- Azure WebUI pipeline (id 6, `StoneSuite - WebUI`): update the
  `VITE_API_BASE_URL` build-step env var from `/api` to
  `https://dev-stonesuite-api.fly.dev/api`.

## Testing

- Table-driven unit test for the CSRF middleware: matching token → pass;
  missing header → 403; mismatched value → 403; `GET`/`HEAD` → exempt;
  `CookieSameSite != "none"` → middleware no-ops regardless of header.
- Manual validation against the deployed dev instance: login → confirm all
  three cookies (`auth_token`, `refresh_token`, `csrf_token`) are actually
  stored by the browser with `Secure; SameSite=None` present in
  `Set-Cookie` → confirm `csrf_token` is readable by JS → confirm a
  mutating request without the header gets 403 → confirm it succeeds with
  the header → confirm the refresh-token flow (wait for/force access-token
  expiry) still silently re-authenticates cross-origin.

## Known issue: dev.stonesuite.app (not in scope)

`dev.stonesuite.app` (GitHub Actions deploy of the `stonesuite` Cloudflare
Pages project) calls `stonesuite-backend.fly.dev` directly, cross-origin,
with `SameSite=Lax` cookies. This means `refresh_token` never round-trips
back to the backend, and users are silently logged out once their access
token expires (masked short-term by the in-memory token + `Authorization`
header fallback). It also means `dev.stonesuite.app` has no environment
isolation — it shares the real production backend.

Fixing this requires the same `SameSite=None` + CSRF change, but applied to
`stonesuite-backend.fly.dev` — the backend real production traffic uses —
which is a materially higher-stakes change than the dev-only work here.
It would also need the same `APP_ENV`/`Secure`-flag prerequisite fix
described above, since `stonesuite-backend`'s `fly.toml` has the identical
gap. Flagged for a separate, deliberate design/rollout decision.

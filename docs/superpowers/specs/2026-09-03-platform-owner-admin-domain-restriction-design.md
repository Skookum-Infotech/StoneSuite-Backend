# Platform-owner admin domain restriction

## Problem

Two privilege mechanisms already exist and both currently grant unrestricted
access with no restriction on who can hold them:

- **Platform admin** (`platform_admins` table, control-plane DB): any
  identity in this table can hit every `/api/platform/*` route — cross-tenant
  admin. Granted only via `AddPlatformAdmin`, currently called from one place
  (`Activate`, `controllers/tenant.go:1537`), but nothing stops a future
  caller from granting it to any identity.
- **Tenant `super_admin`** (`authz` wildcard RBAC grant, `resource=*,
  action=*, scope=all`): full unrestricted access within one tenant. Every
  tenant auto-assigns this to its first user on provisioning, and it can be
  reassigned to other users via the RBAC role-assignment endpoint
  (`controllers/rbac.go:207`, `UserRoles`).

The platform-owner tenant (`tenants.is_platform_owner = true`, seeded once
via `PLATFORM_ADMIN_EMAIL` in `main.go:seedPlatformOwner`) represents
Skookum Infotech's own org on the platform. Only identities with an
`@skookuminfotech.com` email should ever be able to hold platform-admin, or
`super_admin` within that specific tenant. No other tenant's behavior should
change — every other customer tenant continues to auto-assign `super_admin`
to its first user regardless of email domain, exactly as today.

## Non-goals

- No restriction on login generally — non-`skookuminfotech.com` emails can
  still log in and hold non-privileged roles anywhere, including in the
  platform-owner tenant.
- No change to `super_admin` behavior/seeding in any tenant other than the
  platform-owner tenant.
- No change to the generic `authz.Check` / `recordInScope` hot path used by
  every module's reads and writes — this is a grant-time restriction, not a
  per-request one.

## Design

### Config

Add to `config.AppConfig` (`config/config.go`):

```go
PlatformAdminEmailDomain string // env PLATFORM_ADMIN_EMAIL_DOMAIN, default "skookuminfotech.com"
```

A small helper, e.g. `config.AppConfig.EmailMatchesAdminDomain(email string) bool`,
does the case-insensitive `strings.HasSuffix(strings.ToLower(email), "@"+strings.ToLower(domain))`
comparison. Empty domain config disables both gates (fail-open only if an
operator explicitly blanks the env var — documented, not the default).

### Gate 1 — platform admin (`tenancy/platform_admin.go`)

- `AddPlatformAdmin(ctx, identityID)`: look up the identity's email first
  (join `platform_admins` insert against `identities`, or a preceding
  `SELECT email FROM identities WHERE id = $1`). If the domain doesn't
  match, return an error and do not insert. Callers (`Activate` today) must
  surface this as a 4xx, not a 500.
- `IsPlatformAdmin(ctx, identityID)`: change the existence query to join
  `identities` and require the email match:
  ```sql
  SELECT EXISTS(
    SELECT 1 FROM platform_admins pa
    JOIN identities i ON i.id = pa.identity_id
    WHERE pa.identity_id = $1 AND i.email ILIKE '%@' || $2
  )
  ```
  (domain passed as a parameter, never interpolated). This re-validates on
  every read, so a stale or manually-inserted row stops conferring access
  without a data migration. On a domain-mismatch (row exists but denied),
  log a `security_event` (`slog.Warn`) — this is a signal of stale/tampered
  data, distinct from an ordinary "not a platform admin" negative.

### Gate 2 — tenant `super_admin`, platform-owner tenant only

- No new schema — reuse `tenants.is_platform_owner`.
- In `controllers/rbac.go`, `RBACOps.UserRoles` (the role-assignment
  handler): when the role being assigned to a target user is
  `authz.RoleSuperAdmin` **and** the current request's tenant
  (`TenantFromContext`) has `IsPlatformOwner == true`, resolve the target
  identity's email and require it to match `PlatformAdminEmailDomain`.
  Otherwise return 400 with a message like "super_admin in the
  platform-owner tenant is restricted to @<domain> emails." Log a
  `security_event` on denial, consistent with the existing pattern for
  permission denials in this handler.
- This only fires when `IsPlatformOwner == true`, so every other tenant's
  role assignment and first-user auto-provisioning is unaffected.

### What's explicitly out of scope

- `authz.Check`, `authz.IsSuperAdmin`, `recordInScope`: untouched. These are
  the tenant-DB-scoped, high-traffic authorization path used by every
  module; re-validating email domain there would require plumbing the
  caller's email through code that currently only carries `identityID`, and
  is unnecessary once grant-time is gated — a `super_admin` grant can only
  ever be created for a matching email in the first place.
- Provisioning (`provisioning/provisioner.go:242`, `SeedTenantRBAC`): no
  change. It assigns `super_admin` to a brand-new tenant's first user
  regardless of tenant type; for the platform-owner tenant specifically that
  first user is always the bootstrap identity from `PLATFORM_ADMIN_EMAIL`,
  which is already expected to be a matching-domain email operationally (not
  code-enforced at seed time — see Testing below for the one edge case this
  leaves).

## Edge case: bootstrap identity itself

`seedPlatformOwner` creates the platform-owner tenant's first identity
directly from `config.AppConfig.PlatformAdminEmail` — this value is
operator-set, not attacker-controlled, so it is not gated (an operator who
sets `PLATFORM_ADMIN_EMAIL` to a non-matching-domain address has
misconfigured the deployment, not exploited it). `Activate` (which calls
`AddPlatformAdmin` for that same identity) will still enforce Gate 1 as
normal — if an operator sets a mismatched `PLATFORM_ADMIN_EMAIL`, activation
fails loudly at that point rather than silently succeeding, which is the
right fail-closed behavior and surfaces the misconfiguration immediately.

## Testing

- `tenancy/platform_admin_test.go` (new or extended, needs `-tags dbtest`):
  `AddPlatformAdmin` rejects a non-matching-domain email;
  `IsPlatformAdmin` returns false for a pre-existing non-matching row
  inserted directly via SQL (simulating stale data), true for a matching one.
- `controllers/rbac_test.go`: `UserRoles` rejects assigning `super_admin`
  to a non-matching-domain identity when the tenant is the platform-owner
  tenant; allows it when matching; unaffected (allows any domain) when the
  tenant is not the platform-owner tenant.
- Table-driven test for the domain-match helper itself (case-insensitivity,
  subdomain non-match e.g. `evil-skookuminfotech.com` must not match,
  empty-domain-config behavior).

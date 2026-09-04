# Platform-owner admin domain restriction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Only identities whose email ends in `@skookuminfotech.com` (configurable) may ever hold platform-admin (cross-tenant `/api/platform/*` access) or `super_admin` within the platform-owner tenant specifically — gated at the point roles are granted, with runtime re-validation for platform-admin so stale data can't confer access.

**Architecture:** Two independent, additive gates built on existing structures — no new schema. Gate 1 (platform-admin) lives in `tenancy/platform_admin.go`, gated both at grant (`AddPlatformAdmin`) and read (`IsPlatformAdmin`). Gate 2 (tenant `super_admin`) lives in `controllers/rbac.go`'s role-assignment handler, gated only when `tenants.is_platform_owner = true` (an existing flag) — every other tenant's `super_admin` provisioning and assignment is untouched.

**Tech Stack:** Go, pgx/v5, testify (dbtest only), stdlib `testing` for pure-function tests.

## Global Constraints

- No down-migrations; no new schema in this plan — reuses `tenants.is_platform_owner` (`database/migrations/control_plane/schema.sql:21`).
- Never interpolate client values into SQL — all comparisons are parameterized.
- Errors are wrapped with `fmt.Errorf("context: %w", err)`; no `panic()`.
- Security denials are logged via `logSecurityEvent` (controllers package) or `slog.Warn` with a stable `security_event` key (tenancy package, which cannot import controllers).
- Table-driven tests (`testify/assert` in controllers/dbtest files; plain stdlib `testing` is the existing convention for `config` and pure-function tests — see `config/config_test.go`).
- dbtest files carry `//go:build dbtest` and skip cleanly when their DSN env var is unset:
  - `tenancy` package dbtests need `TEST_CP_DATABASE_URL` (control-plane schema).
  - `controllers` package dbtests need **both** `TEST_CP_DATABASE_URL` and `TEST_DATABASE_URL` (tenant schema) when a test touches both the control plane and a tenant pool.
- Files over 300 lines: split them (not expected to be triggered by this plan — all edits are small).

---

## Task 1: Config — platform-admin email domain + match helper

**Files:**
- Modify: `config/config.go:72-74` (struct field), `config/config.go:170-172` (Load wiring), `config/config.go:198` (new helper method, after `IsProduction`)
- Test: `config/config_test.go`

**Interfaces:**
- Produces: `config.Config.PlatformAdminEmailDomain string` (field); `func (c Config) EmailMatchesAdminDomain(email string) bool` — case-insensitive exact-domain match (`user@<domain>`), returns `false` for an empty configured domain (fail-closed) and for any email that doesn't end in exactly `@<domain>` (no subdomain or look-alike match). Consumed by Task 2 (`tenancy/platform_admin.go`) and Task 4 (`controllers/rbac.go`) via `config.AppConfig.EmailMatchesAdminDomain(...)`.

- [ ] **Step 1: Write the failing test**

Add to `config/config_test.go`:

```go
func TestEmailMatchesAdminDomain(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		email  string
		want   bool
	}{
		{"matching domain", "skookuminfotech.com", "staff@skookuminfotech.com", true},
		{"matching domain, mixed case", "skookuminfotech.com", "Staff@Skookuminfotech.COM", true},
		{"non-matching domain", "skookuminfotech.com", "outsider@example.com", false},
		{"look-alike domain is not a match", "skookuminfotech.com", "attacker@evil-skookuminfotech.com", false},
		{"subdomain is not a match", "skookuminfotech.com", "staff@sub.skookuminfotech.com", false},
		{"empty configured domain denies everything", "", "staff@skookuminfotech.com", false},
		{"empty email is denied", "skookuminfotech.com", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{PlatformAdminEmailDomain: tt.domain}
			got := cfg.EmailMatchesAdminDomain(tt.email)
			if got != tt.want {
				t.Errorf("EmailMatchesAdminDomain(%q) with domain %q = %v, want %v", tt.email, tt.domain, got, tt.want)
			}
		})
	}
}

func TestLoadPlatformAdminEmailDomain(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "x")
		Load()
		if AppConfig.PlatformAdminEmailDomain != "skookuminfotech.com" {
			t.Fatalf("default PlatformAdminEmailDomain = %q, want skookuminfotech.com", AppConfig.PlatformAdminEmailDomain)
		}
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "x")
		t.Setenv("PLATFORM_ADMIN_EMAIL_DOMAIN", "example.org")
		Load()
		if AppConfig.PlatformAdminEmailDomain != "example.org" {
			t.Fatalf("PlatformAdminEmailDomain = %q, want example.org", AppConfig.PlatformAdminEmailDomain)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/... -run "TestEmailMatchesAdminDomain|TestLoadPlatformAdminEmailDomain" -v`
Expected: FAIL — `cfg.EmailMatchesAdminDomain` undefined, `AppConfig.PlatformAdminEmailDomain` undefined (compile error).

- [ ] **Step 3: Add the field and Load wiring**

In `config/config.go`, change lines 72-74 from:

```go
	PlatformAdminEmail   string
	PlatformAdminSlug    string
	PlatformAdminCompany string
```

to:

```go
	PlatformAdminEmail   string
	PlatformAdminSlug    string
	PlatformAdminCompany string
	// PlatformAdminEmailDomain restricts who may hold platform-admin, and who
	// may hold super_admin within the platform-owner tenant specifically, to
	// identities whose email ends in "@" + this domain. Every other tenant's
	// super_admin is unaffected. Empty denies everyone (fail-closed).
	PlatformAdminEmailDomain string
```

Change lines 170-172 from:

```go
		PlatformAdminEmail:   getEnv("PLATFORM_ADMIN_EMAIL", ""),
		PlatformAdminSlug:    getEnv("PLATFORM_ADMIN_SLUG", ""),
		PlatformAdminCompany: getEnv("PLATFORM_ADMIN_COMPANY", ""),
```

to:

```go
		PlatformAdminEmail:      getEnv("PLATFORM_ADMIN_EMAIL", ""),
		PlatformAdminSlug:       getEnv("PLATFORM_ADMIN_SLUG", ""),
		PlatformAdminCompany:    getEnv("PLATFORM_ADMIN_COMPANY", ""),
		PlatformAdminEmailDomain: getEnv("PLATFORM_ADMIN_EMAIL_DOMAIN", "skookuminfotech.com"),
```

- [ ] **Step 4: Add the helper method**

In `config/config.go`, immediately after the `IsProduction` method (after line 198's closing `}`), add:

```go
// EmailMatchesAdminDomain reports whether email belongs to the configured
// platform-admin domain (PlatformAdminEmailDomain / PLATFORM_ADMIN_EMAIL_DOMAIN).
// Used to gate who may hold platform-admin or the platform-owner tenant's
// super_admin role. An empty PlatformAdminEmailDomain matches nothing --
// fail-closed, so blanking the env var locks the gate down rather than
// opening it.
func (c Config) EmailMatchesAdminDomain(email string) bool {
	domain := strings.ToLower(strings.TrimSpace(c.PlatformAdminEmailDomain))
	if domain == "" {
		return false
	}
	email = strings.ToLower(strings.TrimSpace(email))
	return strings.HasSuffix(email, "@"+domain)
}
```

(`strings` is already imported in `config/config.go`.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./config/... -run "TestEmailMatchesAdminDomain|TestLoadPlatformAdminEmailDomain" -v`
Expected: PASS (both tests, all subtests).

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 6: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "feat(config): add platform-admin email domain restriction"
```

---

## Task 2: Gate 1 — domain-gate platform-admin grant and read (`tenancy`)

**Files:**
- Modify: `tenancy/platform_admin.go`
- Test: `tenancy/platform_admin_test.go` (new)

**Interfaces:**
- Consumes: `config.AppConfig.EmailMatchesAdminDomain(email string) bool` (Task 1); `(*ControlPlane).IdentityByID(ctx, id) (*Identity, error)` (existing, `tenancy/identity.go:63`) — `Identity.Email` field.
- Produces: `tenancy.ErrAdminDomainNotAllowed error` (new sentinel) — consumed by Task 3 (`controllers/tenant.go`'s `Activate` handler). `AddPlatformAdmin`/`IsPlatformAdmin` signatures are unchanged.

- [ ] **Step 1: Write the failing test**

Create `tenancy/platform_admin_test.go`:

```go
//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"testing"

	"stonesuite-backend/config"
)

// withTestAdminDomain sets config.AppConfig.PlatformAdminEmailDomain for the
// duration of the test, restoring the prior value on cleanup.
func withTestAdminDomain(t *testing.T, domain string) {
	t.Helper()
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = domain
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })
}

func TestAddPlatformAdmin_RejectsNonMatchingDomain(t *testing.T) {
	cp := newCPTestControlPlane(t)
	withTestAdminDomain(t, "skookuminfotech.com")
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	identity, err := cp.CreateIdentity(ctx, tenantID, "someone-"+tenantID+"@example.com", "", "Someone", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	if err := cp.AddPlatformAdmin(ctx, identity.ID); !errors.Is(err, ErrAdminDomainNotAllowed) {
		t.Fatalf("AddPlatformAdmin err = %v, want ErrAdminDomainNotAllowed", err)
	}
	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		t.Fatalf("IsPlatformAdmin: %v", err)
	}
	if isAdmin {
		t.Error("identity must not have been granted platform admin")
	}
}

func TestAddPlatformAdmin_AllowsMatchingDomain(t *testing.T) {
	cp := newCPTestControlPlane(t)
	withTestAdminDomain(t, "skookuminfotech.com")
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	identity, err := cp.CreateIdentity(ctx, tenantID, "admin-"+tenantID+"@skookuminfotech.com", "", "Admin", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	if err := cp.AddPlatformAdmin(ctx, identity.ID); err != nil {
		t.Fatalf("AddPlatformAdmin: %v", err)
	}
	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		t.Fatalf("IsPlatformAdmin: %v", err)
	}
	if !isAdmin {
		t.Error("expected identity to be a platform admin")
	}
}

// TestIsPlatformAdmin_StaleDomainMismatchIsDenied simulates a platform_admins
// row that predates a domain restriction being configured (or was inserted
// directly against the database, bypassing AddPlatformAdmin): IsPlatformAdmin
// must re-validate the email domain on every read, not just trust the row's
// existence.
func TestIsPlatformAdmin_StaleDomainMismatchIsDenied(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	identity, err := cp.CreateIdentity(ctx, tenantID, "stale-"+tenantID+"@example.com", "", "Stale", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	// Bypass AddPlatformAdmin's gate to simulate pre-existing/legacy data.
	if _, err := cp.pool.Exec(ctx, `INSERT INTO platform_admins (identity_id) VALUES ($1)`, identity.ID); err != nil {
		t.Fatalf("seed stale platform_admins row: %v", err)
	}

	withTestAdminDomain(t, "skookuminfotech.com")
	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		t.Fatalf("IsPlatformAdmin: %v", err)
	}
	if isAdmin {
		t.Error("a stale row for a non-matching-domain identity must not confer platform admin")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run (requires `TEST_CP_DATABASE_URL`; skips cleanly otherwise): `go test -tags dbtest ./tenancy/... -run "TestAddPlatformAdmin|TestIsPlatformAdmin_StaleDomainMismatchIsDenied" -v`
Expected: FAIL — `ErrAdminDomainNotAllowed` undefined (compile error), or (once it compiles against old code) `AddPlatformAdmin` does not return the sentinel and the stale row is still reported as admin.

- [ ] **Step 3: Implement the domain gate**

Replace the full contents of `tenancy/platform_admin.go` with:

```go
package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/config"
)

// ErrAdminDomainNotAllowed is returned by AddPlatformAdmin when the target
// identity's email does not match config.AppConfig.PlatformAdminEmailDomain.
// Platform admin is cross-tenant, full-application access, so who can hold it
// is gated at the point of grant, not just at the point of use.
var ErrAdminDomainNotAllowed = errors.New("identity email domain not permitted for platform admin")

// LogPlatformAudit records a cross-tenant platform action.
func (c *ControlPlane) LogPlatformAudit(ctx context.Context, actorID, actorEmail, tenantID, action, detailsJSON string) error {
	var tID any
	if tenantID != "" {
		tID = tenantID
	}
	if detailsJSON == "" {
		detailsJSON = "{}"
	}
	if _, err := c.pool.Exec(ctx, `
		INSERT INTO platform_audit_logs (actor_identity_id, actor_email, tenant_id, action, details)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		nullable(actorID), nullable(actorEmail), tID, action, detailsJSON); err != nil {
		return fmt.Errorf("log platform audit: %w", err)
	}
	return nil
}

// AddPlatformAdmin grants platform-level powers to an identity (idempotent).
// Restricted to identities whose email matches
// config.AppConfig.PlatformAdminEmailDomain -- see ErrAdminDomainNotAllowed.
func (c *ControlPlane) AddPlatformAdmin(ctx context.Context, identityID string) error {
	identity, err := c.IdentityByID(ctx, identityID)
	if err != nil {
		return fmt.Errorf("add platform admin: resolve identity: %w", err)
	}
	if !config.AppConfig.EmailMatchesAdminDomain(identity.Email) {
		return ErrAdminDomainNotAllowed
	}
	if _, err := c.pool.Exec(ctx,
		`INSERT INTO platform_admins (identity_id) VALUES ($1) ON CONFLICT DO NOTHING`, identityID); err != nil {
		return fmt.Errorf("add platform admin: %w", err)
	}
	return nil
}

// IsPlatformAdmin reports whether an identity currently holds platform-level
// powers. Re-validates the identity's email domain on every call (not only at
// grant time in AddPlatformAdmin) so a stale or manually-inserted
// platform_admins row cannot confer access without a data migration.
func (c *ControlPlane) IsPlatformAdmin(ctx context.Context, identityID string) (bool, error) {
	if identityID == "" {
		return false, nil
	}
	var email string
	err := c.pool.QueryRow(ctx, `
		SELECT i.email
		FROM platform_admins pa
		JOIN identities i ON i.id = pa.identity_id
		WHERE pa.identity_id = $1`, identityID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is platform admin: %w", err)
	}
	if !config.AppConfig.EmailMatchesAdminDomain(email) {
		slog.Warn("security event",
			slog.String("security_event", "platform_admin_domain_mismatch"),
			slog.String("identity_id", identityID))
		return false, nil
	}
	return true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags dbtest ./tenancy/... -run "TestAddPlatformAdmin|TestIsPlatformAdmin_StaleDomainMismatchIsDenied" -v`
Expected: PASS (or SKIP if `TEST_CP_DATABASE_URL` unset — acceptable, but prefer running against a real test DB if available).

Run: `go build ./... && go vet ./...`
Expected: builds and vets clean.

- [ ] **Step 5: Commit**

```bash
git add tenancy/platform_admin.go tenancy/platform_admin_test.go
git commit -m "feat(tenancy): gate platform-admin grants to configured email domain"
```

---

## Task 3: Surface the domain gate as 403 in `Activate`

**Files:**
- Modify: `controllers/tenant.go:1536-1540`
- Test: `controllers/platform_activate_dbtest_test.go` (new)

**Interfaces:**
- Consumes: `tenancy.ErrAdminDomainNotAllowed` (Task 2).

- [ ] **Step 1: Write the failing test**

Create `controllers/platform_activate_dbtest_test.go`:

```go
//go:build dbtest

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

// TestTenantOps_Activate_RejectsNonMatchingAdminDomain_DB proves the platform
// activation endpoint surfaces the Task 2 domain gate as 403, not the
// generic 500 an unhandled error would produce, and that the platform-admin
// grant is genuinely never made.
func TestTenantOps_Activate_RejectsNonMatchingAdminDomain_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "skookuminfotech.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })

	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(ctx, "activate-test-"+suffix, "Activate Test", true)
	require.NoError(t, err)
	identity, err := cp.CreateIdentity(ctx, tenant.ID, "outsider-"+suffix+"@example.com", "", "Outsider", false)
	require.NoError(t, err)

	rawToken := "test-token-" + suffix
	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])
	require.NoError(t, cp.SetIdentitySetupTokenHash(ctx, identity.ID, tokenHash, time.Now().Add(15*time.Minute)))

	h := &TenantOps{CP: cp}
	req := httptest.NewRequest(http.MethodPost, "/api/platform/activate", jsonBody(t, map[string]any{
		"token": rawToken, "password": "a-very-long-password-123",
	}))
	rec := httptest.NewRecorder()
	h.Activate(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp["success"].(bool))

	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	require.NoError(t, err)
	assert.False(t, isAdmin, "identity must not have been granted platform admin")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags dbtest ./controllers/... -run TestTenantOps_Activate_RejectsNonMatchingAdminDomain_DB -v`
Expected: FAIL — status is 500 ("Failed to grant admin role."), not 403.

- [ ] **Step 3: Implement**

In `controllers/tenant.go`, change (around line 1536-1540):

```go
	// Grant platform-admin role.
	if err := h.CP.AddPlatformAdmin(r.Context(), identity.ID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to grant admin role.")
		return
	}
```

to:

```go
	// Grant platform-admin role.
	if err := h.CP.AddPlatformAdmin(r.Context(), identity.ID); err != nil {
		if errors.Is(err, tenancy.ErrAdminDomainNotAllowed) {
			fail(w, http.StatusForbidden, "This email is not permitted to hold platform admin.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to grant admin role.")
		return
	}
```

(`errors` and `tenancy` are already imported in `controllers/tenant.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags dbtest ./controllers/... -run TestTenantOps_Activate_RejectsNonMatchingAdminDomain_DB -v`
Expected: PASS (or SKIP if `TEST_CP_DATABASE_URL` unset).

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 5: Commit**

```bash
git add controllers/tenant.go controllers/platform_activate_dbtest_test.go
git commit -m "fix(controllers): return 403 when the platform-admin domain gate denies activation"
```

---

## Task 4: Gate 2 — restrict `super_admin` assignment in the platform-owner tenant

**Files:**
- Modify: `controllers/rbac.go`
- Test: `controllers/rbac_admin_domain_test.go` (new, pure function, no build tag)

**Interfaces:**
- Consumes: `config.AppConfig.EmailMatchesAdminDomain` (Task 1); `tenancy.Tenant.IsPlatformOwner bool` (existing, `tenancy/tenant.go:40`); `tenancy.TenantFromContext(ctx) (*tenancy.Tenant, error)` (existing, `tenancy/middleware.go:77`); `authz.GetRole(ctx, q, id) (*authz.Role, error)` + `authz.Role.Key`/`authz.RoleSuperAdmin` (existing); `userstore.GetUserByID(ctx, q, id) (*userstore.User, error)` + `userstore.User.Email` (existing, `userstore/store.go:103`).
- Produces: `superAdminAssignmentAllowed(tenant *tenancy.Tenant, targetEmail string) bool` (new, unexported) — consumed only within `controllers/rbac.go`; also exercised directly by Task 4's own test and indirectly by Task 5's end-to-end test.

- [ ] **Step 1: Write the failing test**

Create `controllers/rbac_admin_domain_test.go`:

```go
package controllers

import (
	"testing"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

func TestSuperAdminAssignmentAllowed(t *testing.T) {
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "skookuminfotech.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })

	tests := []struct {
		name            string
		isPlatformOwner bool
		email           string
		want            bool
	}{
		{"non-platform-owner tenant: any email allowed", false, "anyone@example.com", true},
		{"platform-owner tenant: matching domain allowed", true, "staff@skookuminfotech.com", true},
		{"platform-owner tenant: matching domain, mixed case allowed", true, "Staff@Skookuminfotech.COM", true},
		{"platform-owner tenant: non-matching domain denied", true, "outsider@example.com", false},
		{"platform-owner tenant: look-alike domain denied", true, "attacker@evil-skookuminfotech.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenant := &tenancy.Tenant{IsPlatformOwner: tt.isPlatformOwner}
			got := superAdminAssignmentAllowed(tenant, tt.email)
			if got != tt.want {
				t.Errorf("superAdminAssignmentAllowed(IsPlatformOwner=%v, %q) = %v, want %v",
					tt.isPlatformOwner, tt.email, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./controllers/... -run TestSuperAdminAssignmentAllowed -v`
Expected: FAIL — `superAdminAssignmentAllowed` undefined (compile error).

- [ ] **Step 3: Implement the helper and wire it into `UserRoles`**

In `controllers/rbac.go`, add this function after the `authorize` method (after line 50's closing `}`):

```go
// superAdminAssignmentAllowed reports whether targetEmail may hold
// super_admin in the given tenant. Only the platform-owner tenant restricts
// super_admin to config.AppConfig.PlatformAdminEmailDomain -- every other
// tenant's super_admin (auto-assigned to its first user on provisioning) is
// unrestricted, exactly as before this gate existed.
func superAdminAssignmentAllowed(tenant *tenancy.Tenant, targetEmail string) bool {
	if !tenant.IsPlatformOwner {
		return true
	}
	return config.AppConfig.EmailMatchesAdminDomain(targetEmail)
}
```

Then in `UserRoles`, change the `http.MethodPost` case (currently lines 228-240) from:

```go
	case http.MethodPost:
		var req struct {
			RoleID string `json:"roleId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoleID == "" {
			fail(w, http.StatusBadRequest, "roleId is required.")
			return
		}
		if err := authz.AssignRole(r.Context(), pool, userID, req.RoleID); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to assign role.")
			return
		}
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Role assigned."})
```

to:

```go
	case http.MethodPost:
		var req struct {
			RoleID string `json:"roleId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoleID == "" {
			fail(w, http.StatusBadRequest, "roleId is required.")
			return
		}
		role, err := authz.GetRole(r.Context(), pool, req.RoleID)
		if err != nil && !errors.Is(err, authz.ErrRoleNotFound) {
			fail(w, http.StatusInternalServerError, "Failed to load role.")
			return
		}
		if err == nil && role.Key == authz.RoleSuperAdmin {
			tenant, tErr := tenancy.TenantFromContext(r.Context())
			if tErr != nil {
				fail(w, http.StatusInternalServerError, "Tenant not resolved.")
				return
			}
			targetUser, uErr := userstore.GetUserByID(r.Context(), pool, userID)
			if uErr != nil {
				fail(w, http.StatusBadRequest, "Target user not found.")
				return
			}
			if !superAdminAssignmentAllowed(tenant, targetUser.Email) {
				logSecurityEvent(r, "super_admin_assignment_denied", "tenant_id", tenant.ID, "target_user_id", userID)
				fail(w, http.StatusBadRequest, "super_admin in this workspace is restricted to "+config.AppConfig.PlatformAdminEmailDomain+" emails.")
				return
			}
		}
		if err := authz.AssignRole(r.Context(), pool, userID, req.RoleID); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to assign role.")
			return
		}
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Role assigned."})
```

(`authz`, `config`, `tenancy`, `userstore` are already imported in `controllers/rbac.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./controllers/... -run TestSuperAdminAssignmentAllowed -v`
Expected: PASS (all subtests).

Run: `go build ./... && go vet ./...`
Expected: builds and vets clean.

- [ ] **Step 5: Commit**

```bash
git add controllers/rbac.go controllers/rbac_admin_domain_test.go
git commit -m "feat(controllers): restrict super_admin assignment in the platform-owner tenant to the admin domain"
```

---

## Task 5: End-to-end proof of the `super_admin` gate

**Files:**
- Test: `controllers/rbac_admin_domain_dbtest_test.go` (new)

**Interfaces:**
- Consumes: `seedServableCustomerTestTenant` (`controllers/customer_auth_dbtest_test.go:43`), `testCustomerTenantPool` (`controllers/customer_auth_dbtest_test.go:27`), `newSAMLTestControlPlane` (`controllers/saml_dbtest_test.go:38`), `seedRBACTestIdentity`/`seedRBACTestRole` (`controllers/rbac_permission_refresh_dbtest_test.go:38,57`), `authz.SeedSuperAdmin(ctx, q) (string, error)` (existing, `authz/seed.go:15`).
- Produces: `seedServablePlatformOwnerTestTenant(t, cp, dsn) *tenancy.Tenant` (new test helper, package-local to `controllers`, mirrors `seedServableCustomerTestTenant` with `isPlatformOwner=true`).

This task has no production code change — it only adds coverage proving Task 4's wiring works through the real HTTP + middleware chain (not just the pure helper function), and that non-platform-owner tenants are genuinely unaffected.

- [ ] **Step 1: Write the test**

Create `controllers/rbac_admin_domain_dbtest_test.go`:

```go
//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// seedServablePlatformOwnerTestTenant mirrors seedServableCustomerTestTenant
// (customer_auth_dbtest_test.go) but creates a platform-owner tenant
// (is_platform_owner = true), so tests can exercise the super_admin domain
// gate that only fires for that tenant.
func seedServablePlatformOwnerTestTenant(t *testing.T, cp *tenancy.ControlPlane, dsn string) *tenancy.Tenant {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(ctx, "owner-rbac-test-"+suffix, "Owner RBAC Test Tenant", true)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "owner_rbac_test_db", dsn, 1))
	got, err := cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)
	require.True(t, got.Servable(), "seeded tenant must be Servable()")
	require.True(t, got.IsPlatformOwner)
	return got
}

// assignRoleRequest drives RBACOps.UserRoles through the real Resolver
// middleware (not a hand-built context), matching the established pattern in
// rbac_permission_refresh_dbtest_test.go.
func assignRoleRequest(t *testing.T, resolver *tenancy.Resolver, rbacOps *RBACOps, actorIdentityID, actorTenantID, targetUserID, roleID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/users/"+targetUserID+"/roles",
		jsonBody(t, map[string]any{"roleId": roleID}))
	payload := middleware.UserContextPayload{ID: actorIdentityID, TenantID: actorTenantID}
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, payload)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	resolver.Middleware(http.HandlerFunc(rbacOps.UserRoles)).ServeHTTP(rec, req)
	return rec
}

// TestRBACOps_UserRoles_SuperAdminDomainGate_DB proves the two halves of the
// gate together: within the platform-owner tenant, assigning super_admin to
// a non-matching-domain user is denied while a matching-domain user is
// allowed; a normal (non-platform-owner) tenant is completely unaffected.
func TestRBACOps_UserRoles_SuperAdminDomainGate_DB(t *testing.T) {
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "skookuminfotech.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })

	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	rbacOps := NewRBACOps()
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	grantUpdatePermission := func(actorUserID string) {
		roleID := seedRBACTestRole(t, pool, "role-updater", authz.Grant{
			Resource: authz.ResourceRole, Action: authz.ActionUpdate, Scope: authz.ScopeAll,
		})
		require.NoError(t, authz.AssignRole(context.Background(), pool, actorUserID, roleID))
	}

	t.Run("platform-owner tenant denies a non-matching-domain target", func(t *testing.T) {
		tenant := seedServablePlatformOwnerTestTenant(t, cp, dsn)
		actor, actorUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "actor-password")
		grantUpdatePermission(actorUser.ID)

		_, targetUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "target-password") // @example.com
		superAdminRoleID, err := authz.SeedSuperAdmin(context.Background(), pool)
		require.NoError(t, err)

		rec := assignRoleRequest(t, resolver, rbacOps, actor.ID, tenant.ID, targetUser.ID, superAdminRoleID)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	})

	t.Run("platform-owner tenant allows a matching-domain target", func(t *testing.T) {
		tenant := seedServablePlatformOwnerTestTenant(t, cp, dsn)
		actor, actorUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "actor-password")
		grantUpdatePermission(actorUser.ID)

		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		targetIdentity, err := cp.CreateIdentity(context.Background(), tenant.ID,
			fmt.Sprintf("staff-%s@skookuminfotech.com", suffix), "", "Staff", true)
		require.NoError(t, err)
		targetUser, err := userstore.CreateUser(context.Background(), pool, targetIdentity.ID, targetIdentity.Email, "Staff", "active")
		require.NoError(t, err)

		superAdminRoleID, err := authz.SeedSuperAdmin(context.Background(), pool)
		require.NoError(t, err)

		rec := assignRoleRequest(t, resolver, rbacOps, actor.ID, tenant.ID, targetUser.ID, superAdminRoleID)
		assert.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	})

	t.Run("non-platform-owner tenant is unaffected", func(t *testing.T) {
		tenant := seedServableCustomerTestTenant(t, cp, dsn)
		actor, actorUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "actor-password")
		grantUpdatePermission(actorUser.ID)

		_, targetUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "target-password") // @example.com, not skookuminfotech.com
		superAdminRoleID, err := authz.SeedSuperAdmin(context.Background(), pool)
		require.NoError(t, err)

		rec := assignRoleRequest(t, resolver, rbacOps, actor.ID, tenant.ID, targetUser.ID, superAdminRoleID)
		assert.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	})
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test -tags dbtest ./controllers/... -run TestRBACOps_UserRoles_SuperAdminDomainGate_DB -v`
Expected: PASS, all three subtests (or SKIP if `TEST_CP_DATABASE_URL`/`TEST_DATABASE_URL` unset).

Run: `go build ./... && go vet ./...`
Expected: builds and vets clean.

- [ ] **Step 3: Commit**

```bash
git add controllers/rbac_admin_domain_dbtest_test.go
git commit -m "test(controllers): end-to-end coverage for the super_admin domain gate"
```

---

## Final verification

- [ ] Run the full non-dbtest suite: `go test ./...` — expect PASS.
- [ ] Run the full dbtest suite if `TEST_CP_DATABASE_URL`/`TEST_DATABASE_URL` are available: `go test -tags dbtest ./...` — expect PASS.
- [ ] Run `go vet ./...` — expect clean.
- [ ] Run `golangci-lint run` if installed — expect clean.
- [ ] Dispatch the `tenancy-security-reviewer` agent against the diff (multi-tenancy/RBAC-sensitive change, per `CLAUDE.md`).

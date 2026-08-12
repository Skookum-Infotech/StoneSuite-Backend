# SAML SSO Backend Implementation Plan — StoneSuite Backend

**Date:** 2026-08-07  
**Branch:** `feat/saml-sso-auth` (created from `develop`)  
**Status:** Awaiting approval  

---

## Executive Summary

Implement production-ready **SAML 2.0 SSO authentication** for AWS Cognito and Microsoft Entra ID as identity providers in StoneSuite Backend. The implementation:

- Adds SAML assertion parsing, signature verification, and attribute extraction
- Creates SAML-specific endpoints (ACS/Assertion Consumer Service, metadata serving, login initiation)
- Extends the existing `tenant_sso_configs` schema to store SAML-specific fields (IdP metadata URL, certificate, entity ID, etc.)
- Implements identity linking (create-on-first-login or link existing email) with full RBAC/tenant scope integration
- Maintains backward compatibility with existing password-based and OIDC-OAuth configuration storage
- Includes comprehensive audit logging, error handling, and security validation
- Requires real AWS Cognito and Microsoft Entra test configuration for end-to-end validation

The implementation follows StoneSuite's multi-tenancy model (separate tenant databases, control-plane identity registry), existing patterns (config CRUD, JWT minting, security logging), and CLAUDE.md constraints (no down-migrations, idempotent schema, parameterized queries, explicit tenant scoping).

---

## Problem Statement

**Current State (Phase 0 Investigation):**
- Backend has zero SAML code; zero SAML libraries in `go.mod`
- Existing SSO config storage (CRUD endpoints, `tenant_sso_configs` table) is OIDC-shaped only (`client_id`, `client_secret`, `issuer`, `redirect_uri`); no actual SSO login flow
- Old OAuth2 login implementation (deleted 2026-06-03 during monorepo split) was never rebuilt for multi-tenancy
- Frontend has UI for "SAML Setup" with two disconnected surfaces: working OIDC config form + non-functional SAML walkthrough (disabled Save button)
- No tenant-scoped identity linking, no SAML assertion parser, no ACS endpoint, no metadata serving

**User Requirements:**
1. Implement complete SAML 2.0 SSO flow for AWS Cognito and Microsoft Entra ID
2. Support both login initiation and logout
3. Validate SAML assertions (XML signature, certificate chain)
4. Link SAML identity to local identity (create-on-first-login or link-by-email)
5. Integrate with existing RBAC, tenant scoping, audit logging
6. Test end-to-end with real AWS Cognito and Entra ID configurations
7. Document all required environment variables and test setup

---

## Architecture

### Design Principles

1. **Tenant isolation by construction** — every SAML assertion belongs to a tenant; no implicit cross-tenant reads
2. **Identity linking strategy** — accept email from SAML assertion, match to existing identity in control-plane (by email) or create new one; link `sso_provider` / `sso_subject` on first login
3. **Reuse existing infrastructure** — JWT minting, security logging, RBAC, identity model, config encryption
4. **Multi-provider support** — AWS Cognito and Entra ID both implement SAML 2.0 standard; single implementation path for both
5. **Stateless assertions** — SAML assertion validation is synchronous; no server-side state machine needed

### High-Level Flow

```
1. User clicks "Sign in with SAML" on frontend (tenant context TBD — see Risks)
   ↓
2. Browser redirected to /api/auth/saml/{provider}/initiate
   (provider: "cognito" or "entra")
   ↓
3. Backend constructs AuthnRequest XML, signs it, redirects to IdP SSO endpoint
   ↓
4. IdP authenticates user, returns SAML Response (signed assertion) to ACS endpoint
   ↓
5. POST to /api/auth/saml/{provider}/acs
   ↓
6. Backend:
   a) Parse SAML Response XML
   b) Verify XML signature against IdP certificate
   c) Extract email, name, and provider-specific attributes (subject, session index)
   d) Identity linking: look up or create local identity; set sso_provider/sso_subject
   e) Create JWT, set httpOnly cookie, redirect to frontend with token
   ↓
7. Frontend redirects to dashboard, uses JWT for authenticated requests
   ↓
8. Logout endpoint (/api/auth/saml/{provider}/logout) sends SAML LogoutRequest to IdP SLO endpoint
```

### Components to Implement

#### 1. **SAML Service Package** (`saml/` or `sso/saml.go`)
Stateless, provider-agnostic SAML helpers:
- `SAMLMetadata(spEntityID string, acsURL string) -> XML` — generate SP metadata
- `BuildAuthnRequest(idpSSOURL, spEntityID, acsURL, relayState) -> (signedXML, ID)`
- `ParseSAMLResponse(responseXML, idpCertPEM) -> (email, attributes, sessionIndex, err)`
- `ValidateSAMLSignature(responseXML, idpCertPEM) -> (bool, err)`
- `BuildLogoutRequest(idpSLOURL, sessionIndex, nameID) -> (signedXML, ID)`

**Library:** Use `github.com/russellhaering/gosaml2` (well-maintained, used in production, handles XML-dsig spec compliance). Alternatives: `crewjam/saml` (heavier, more opinionated), `amdonov/lite-saml` (lighter, less complete).

#### 2. **SAML Config Extensions** (`tenancy/saml_config.go` or extend `sso_config.go`)
Schema additions to `tenant_sso_configs`:
- `metadata_url` (TEXT) — IdP metadata document endpoint (e.g., AWS metadata URL)
- `idp_entity_id` (TEXT) — IdP Entity ID from metadata (extracted on save)
- `sso_url` (TEXT) — IdP SSO endpoint from metadata
- `slo_url` (TEXT, nullable) — IdP SLO endpoint if provided
- `certificate_pem` (TEXT) — IdP X.509 certificate in PEM format (extracted from metadata or uploaded)
- `certificate_fingerprint` (TEXT) — SHA-256 fingerprint of certificate (for audit/lookup)
- `name_id_format` (VARCHAR(255), default "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress")
- `protocol` (VARCHAR(50)) — 'saml' or 'oidc' to distinguish; existing configs = 'oidc', new SAML = 'saml'
- `metadata_fetched_at` (TIMESTAMPTZ, nullable) — track when IdP metadata was last refreshed

Extend `SSOConfig` struct to include SAML fields (with omitempty JSON tags so existing OIDC configs don't bloat).

#### 3. **Metadata Endpoint** (`/api/tenant/saml/{provider}/metadata`)
GET endpoint that returns SP metadata as XML:
- `<EntityDescriptor entityID="https://app.stonesuite.io/saml/{provider}/metadata">`
- `<SPSSODescriptor>` with ACS endpoints for each supported binding (HTTP-POST, HTTP-Redirect)
- `<SingleLogoutService>` with SLO URL
- No certificate in metadata (SP doesn't sign responses)

**Authorization:** Public (no auth required) — IdP needs to fetch without JWT

#### 4. **Login Initiation Endpoint** (`/api/auth/saml/{provider}/initiate`)
GET or POST. Parameters:
- `tenant_slug` or `tenant_id` (query param) — identifies which tenant is signing in (CRITICAL: see Risks section)
- Optional `RelayState` for frontend deep-link post-login

Flow:
1. Validate tenant exists and has SAML config enabled
2. Load IdP configuration (entity_id, sso_url, certificate)
3. Generate AuthnRequest with signed XML, unique request ID
4. **Store request state** (request ID → tenant, timestamp) in memory cache or short-lived DB table for validation on callback (prevents CSRF, enforces single-use, ties response back to tenant)
5. Redirect to `{idp.sso_url}?SAMLRequest={base64-encoded-authn-request}&RelayState={...}`

**State storage:** In-memory with TTL or short-lived DB table (`saml_requests`); must include tenant_id so callback can resolve tenant without trusting client.

#### 5. **Assertion Consumer Service (ACS) Endpoint** (`/api/auth/saml/{provider}/acs`)
POST endpoint. SAML IdP POSTs to this with signed assertion.

Flow:
1. Parse SAML Response XML
2. Validate request ID matches stored state (CSRF check, tenant resolution)
3. Verify XML signature using IdP certificate
4. Extract claims: `Email` (NameID), `Name` (attribute), `Subject` (NameID/persistent ID), `SessionIndex`
5. **Identity linking:**
   - Query `identities` by email (case-insensitive)
   - If found: update `sso_provider` / `sso_subject` (idempotent)
   - If not found: create new identity with `sso_provider` / `sso_subject`, email verified=true, no password
6. Generate JWT (include tenant_id from linked identity)
7. Set httpOnly cookie + return token to frontend
8. Redirect to frontend dashboard (or RelayState URL)
9. Log security event: success or failure (invalid signature, assertion expired, subject mismatch)

**Error handling:**
- Invalid XML: 400 (client error)
- Signature validation failed: 400 + security log (potential attack)
- Subject mismatch (sso_subject changes): 403 + security log (IDOR attempt or credential compromise)
- Email already linked to another tenant: 409 (conflict; see Risks)
- Assertion expired: 400 (user took too long; retry)

#### 6. **Logout Endpoint** (`/api/auth/saml/{provider}/logout`)
POST endpoint. Authenticated users can initiate SAML SLO.

Flow:
1. Verify JWT (RequireAuth middleware)
2. Look up session data: last SessionIndex from identity (stored when ACS creates JWT)
3. Build SAML LogoutRequest signed with SP private key (if required by IdP; most don't)
4. Redirect to `{idp.slo_url}?SAMLRequest={...}`
5. IdP redirects back to LogoutResponse URL
6. Frontend clears JWT/cookie

**Note:** SAML SLO is best-effort; not all IdPs implement it reliably. Fallback is standard JWT expiry.

#### 7. **Identity Extension** (`tenancy/identity.go`)
Add methods to `ControlPlane`:
- `LinkSSOIdentity(ctx, identityID, ssoProvider, ssoSubject) error` — set sso_provider/sso_subject on existing identity (idempotent)
- `IdentityBySSO(ctx, ssoProvider, ssoSubject) -> *Identity` — lookup by SSO subject (used for repeat logins)

#### 8. **Configuration Schema Changes**
Control-plane migration (`database/migrations/control_plane/schema.sql`):

```sql
-- Extend tenant_sso_configs with SAML-specific columns
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS protocol VARCHAR(50) DEFAULT 'oidc';
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS metadata_url TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS idp_entity_id TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS sso_url TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS slo_url TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS certificate_pem TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS certificate_fingerprint TEXT;
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS name_id_format VARCHAR(255) DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress';
ALTER TABLE tenant_sso_configs ADD COLUMN IF NOT EXISTS metadata_fetched_at TIMESTAMPTZ;

-- Optional: SAML request state tracking (short-lived, for CSRF prevention)
CREATE TABLE IF NOT EXISTS saml_requests (
    id TEXT PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_saml_requests_expires ON saml_requests(expires_at);
```

---

## Implementation Steps (Sequential)

### Step 1: Add SAML Library & Configuration
**Sequential (blocking later steps)**

- Add `github.com/russellhaering/gosaml2` to `go.mod` (`go get github.com/russellhaering/gosaml2@latest`)
- Add optional SAML-related env vars to `config/config.go`:
  - `SAML_SP_ENTITY_ID` (e.g., `https://app.stonesuite.io/saml`)
  - `SAML_IDP_METADATA_REFRESH_INTERVAL` (e.g., `24h`, for cached IdP metadata)
  - `SAML_REQUEST_TIMEOUT` (e.g., `10m`, assertion expiry tolerance)
- No changes to existing AppConfig; these are optional/informational

### Step 2: Database Migrations
**Sequential (blocking schema-dependent code)**

- Create/append idempotent ALTER TABLE statements to `database/migrations/control_plane/schema.sql`
- Add `saml_requests` table for CSRF state tracking
- Verify idempotency: running migrations twice should succeed silently
- Run locally: `TEST_DATABASE_URL=... go test -tags dbtest ./...`

### Step 3: SAML Service Package
**Independent (no DB, can be tested with unit tests)**

- Create `saml/saml.go` (or extend existing `sso/` package if one exists)
- Implement helpers:
  - XML parsing (gosaml2 handles this)
  - AuthnRequest building + signing
  - SAML Response parsing + signature validation
  - Metadata generation
  - LogoutRequest building
- Unit tests with table-driven test cases (valid assertions, invalid signatures, expired assertions, malformed XML, etc.)
- Do NOT call database or HTTP; pure functions only

### Step 4: Tenancy / Identity Linking
**Sequential (depends on Step 2 schema)**

- Extend `tenancy/identity.go`:
  - Add `LinkSSOIdentity()` method
  - Add `IdentityBySSO()` method for repeat logins
  - Add `IdentityByEmailForSSO()` (case-insensitive, used for linking)
- Extend `tenancy/sso_config.go`:
  - Rename/refactor to split OIDC and SAML concerns (or add SAML struct alongside OIDC)
  - Add SAML-specific fields to `SSOConfig` struct
  - Add `GetSSOConfigBySAML()` (load by provider, protocol='saml')
  - Add `UpdateSAMLMetadata()` (fetch/store IdP metadata periodically)
- Unit tests with test DB (`-tags dbtest`)

### Step 5: Config CRUD Extensions
**Sequential (depends on Step 4)**

- Extend `controllers/sso.go`:
  - Modify `validateSSORequest()` to accept SAML-specific fields (metadata_url, certificate, etc.)
  - On create: if `protocol='saml'`, fetch IdP metadata, extract fields, validate certificate
  - On update: same validation
  - Encrypt `certificate_pem` like `client_secret`? (Decision: store encrypted, decrypt only on assertion validation)
  - Return SAML fields in responses (populated for SAML configs, omitted for OIDC)
- Extend `tenancy/sso_config.go` CRUD methods to handle SAML columns
- Update `SSOOps` to handle protocol field
- Tests: unit + dbtest

### Step 6: SAML Endpoints (Initiate, ACS, Logout, Metadata)
**Sequential (depends on Steps 3, 4, 5)**

- Create `controllers/saml_auth.go` (or extend tenant.go) with handlers:
  - `SamlMetadata(w, r)` — GET `/api/tenant/saml/{provider}/metadata` (public, no auth)
  - `SamlInitiate(w, r)` — GET/POST `/api/auth/saml/{provider}/initiate` (public, query param: tenant slug/id)
  - `SamlACS(w, r)` — POST `/api/auth/saml/{provider}/acs` (public, SAML IdP posts here)
  - `SamlLogout(w, r)` — POST `/api/auth/saml/{provider}/logout` (auth required)
- Implement state tracking (SAML request storage):
  - `StoreSAMLRequest()` in memory or DB
  - `ValidateSAMLRequest()` (CSRF check, state cleanup)
- Identity linking logic:
  - Query existing identity by email
  - Decide: create new if missing, or require pre-provisioning? (Recommendation: create-on-first-login for UX)
  - Update `sso_provider` / `sso_subject` on success
  - Generate JWT (reuse existing `generateTenantJWT()`)
- Comprehensive error handling + security logging for each failure path
- Tests: unit, integration (with mock IdP assertions), and dbtest

### Step 7: Route Wiring (main.go)
**Sequential (depends on Step 6)**

- Add routes to `main.go`:
  - `mux.HandleFunc("GET /api/tenant/saml/{provider}/metadata", samlAuth.Metadata)` (public)
  - `mux.HandleFunc("GET /api/auth/saml/{provider}/initiate", ...InitiateFunc)` (public, rate-limited by IP)
  - `mux.HandleFunc("POST /api/auth/saml/{provider}/acs", ...ACSFunc)` (public, rate-limited by IP)
  - `mux.Handle("POST /api/auth/saml/{provider}/logout", RequireAuth(...Logout))` (auth required)
- Implement rate limiting on public endpoints (reuse `authRateLimiter` pattern)
- Ensure `/api/tenant/saml/...` routes do NOT have TenantResolver (tenant comes from SAML assertion or URL param, not JWT)

### Step 8: Security Logging & Audit
**Sequential (depends on Step 6)**

- Add security events for all SAML flows:
  - `saml_login_initiated` (provider, tenant)
  - `saml_assertion_validated` (provider, email)
  - `saml_identity_created` (provider, email, identity_id)
  - `saml_identity_linked` (provider, email, identity_id, existing: true/false)
  - `saml_signature_invalid` (provider, reason)
  - `saml_assertion_expired` (provider)
  - `saml_logout_initiated` (provider, identity_id)
- Use `logSecurityEvent(r, "event_name", kv...)` pattern
- Do NOT log SAML assertions, certificates, or email addresses (only identity IDs)

### Step 9: Configuration Admin Endpoints (Frontend Integration)
**Sequential (depends on Step 5)**

- Extend `CreateConfig` / `UpdateConfig` to accept SAML protocol + fields
- Frontend will call: `POST /api/tenant/sso-configs` with `{protocol: 'saml', provider: 'cognito', metadata_url: '...', ...}`
- On save: fetch IdP metadata, extract certificate + SSO/SLO URLs, validate
- Return SAML config with extracted fields (so frontend can display)
- Handle 503 if `SECRET_ENCRYPTION_KEY` not set (certificate encryption)
- Add RBAC check (reuse `sso_config` resource, `configure` action)

### Step 10: Tests
**Sequential (last, validates all steps)**

- Unit tests for `saml/` package (50+ cases: valid assertions, signature failures, expired, malformed XML, etc.)
- Integration tests for endpoints (mock IdP responses, token validation, identity linking, JWT generation)
- DB tests with real control-plane DB (`-tags dbtest`):
  - SSO config CRUD with SAML fields
  - Identity creation + linking
  - Multi-tenant isolation (config from tenant A can't be accessed by tenant B)
- End-to-end with real AWS Cognito + Entra ID (manual testing; see Testing Strategy section)
- All tests pass: `go test ./...` and `go test -tags dbtest ./...`

### Step 11: Documentation & Environment Variables
**Sequential (last, validates deployment setup)**

- Update `docs/api/config-endpoints.md` with SAML config schema
- Update `.env.example` and `.env` with new optional vars
- Create `docs/SAML_SETUP.md` with:
  - AWS Cognito SAML setup walkthrough (create IdP app, download metadata, upload to StoneSuite)
  - Microsoft Entra ID SAML setup walkthrough (app registration, enterprise app, SAML config)
  - Environment variables (SECRET_ENCRYPTION_KEY required for storing certificates)
  - Testing guide (real Cognito/Entra tenant + test account)
  - Troubleshooting (signature validation failures, certificate expiry, email mapping issues)
- Update main README with link to SAML docs

### Step 12: Cleanup & Verification
**Sequential (final)**

- Remove dead OAuth env vars from `config.go` (EntraIDClientID, CognitoClientID, etc.)
- Verify no leftover `test_oauth.ps1` / `test_auth.ps1` routes (these are stale)
- Run full test suite: `go test ./...` and `go test -tags dbtest ./...`
- Build check: `go build ./...`
- Lint: `golangci-lint run`

---

## Affected Files (Comprehensive)

### New Files
- `saml/saml.go` — SAML service (parser, builder, validator)
- `saml/saml_test.go` — unit tests
- `controllers/saml_auth.go` — SAML endpoint handlers
- `controllers/saml_auth_test.go` — endpoint tests
- `docs/superpowers/plans/2026-08-07-saml-sso-backend.md` — this plan
- `docs/SAML_SETUP.md` — deployment guide

### Modified Files
- `go.mod` — add `github.com/russellhaering/gosaml2` (and its deps: `etree`, `dsig`)
- `database/migrations/control_plane/schema.sql` — ALTER TABLE for SAML columns, CREATE TABLE for saml_requests
- `config/config.go` — add optional SAML config (SAML_SP_ENTITY_ID, etc.)
- `tenancy/identity.go` — add `LinkSSOIdentity()`, `IdentityBySSO()`, `IdentityByEmailForSSO()`
- `tenancy/sso_config.go` — extend SSOConfig struct, add SAML CRUD methods
- `controllers/sso.go` — extend CRUD handlers for SAML fields, validation
- `controllers/tenant.go` — possibly move SAML handlers here, or keep separate
- `main.go` — add SAML routes
- `.env.example` / `.env` — add optional env vars (no secrets, just informational keys)
- `go.sum` — updated by `go get`

### Unchanged (But Referenced)
- `middleware/auth.go` — JWT validation (reused as-is)
- `authz/catalog.go` — `ResourceSSOConfig` permission (reused for SAML config CRUD)
- `controllers/security_log.go` — `logSecurityEvent()` helper (reused)

---

## Dependency Changes

### New Dependencies
```
github.com/russellhaering/gosaml2 v0.X.Y (to be determined by go get)
  └─ requires: github.com/beevik/etree (XML parsing)
  └─ requires: github.com/russellhaering/goxmldsig (XML digital signature)
```

### Version Constraints
- Go 1.21+ (already required by project)
- PostgreSQL 13+ (idempotent ALTER TABLE support, COALESCE)

### Removed Dependencies
- None (backward compatible; OIDC configs coexist with SAML)

---

## Data Model Changes

### `tenant_sso_configs` Table (Control-Plane)
**Current columns:** `id`, `tenant_id`, `provider`, `client_id`, `client_secret_enc`, `issuer`, `redirect_uri`, `enabled`, `created_at`, `updated_at`

**New columns (SAML-specific, nullable for backward compatibility):**
- `protocol VARCHAR(50) DEFAULT 'oidc'` — 'oidc' or 'saml' (for future-proofing; existing = 'oidc')
- `metadata_url TEXT` — URL to IdP's SAML metadata document
- `idp_entity_id TEXT` — IdP's Entity ID (extracted from metadata)
- `sso_url TEXT` — IdP's SSO endpoint URL (extracted from metadata)
- `slo_url TEXT` — IdP's Single Logout endpoint (optional, extracted from metadata)
- `certificate_pem TEXT` — IdP's X.509 certificate in PEM format (extracted from metadata, encrypted at rest)
- `certificate_fingerprint TEXT` — SHA-256 fingerprint (for quick validation, audit trail)
- `name_id_format VARCHAR(255) DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress'` — NameID format
- `metadata_fetched_at TIMESTAMPTZ` — timestamp of last metadata fetch (for cache invalidation)

**Rationale:** These fields are SAML-specific and OIDC configs don't use them (left NULL). Mixing protocols in one table simplifies multi-protocol support without needing a new table. Queries filtering by `protocol='saml'` efficiently isolate SAML work.

### `identities` Table (Control-Plane)
**Existing SAML columns (unused until now):**
- `sso_provider VARCHAR(50)` — 'cognito', 'entra', etc.
- `sso_subject TEXT` — opaque subject ID from SAML assertion (persistent, globally unique within IdP)

**New usage:** On successful SAML ACS, set these fields to link the identity to its SAML subject (enables repeat logins without re-asserting).

### New Table: `saml_requests` (Control-Plane)
For CSRF state tracking on AuthnRequest → Response flow:

```sql
CREATE TABLE IF NOT EXISTS saml_requests (
    id TEXT PRIMARY KEY,              -- gosaml2-generated request ID
    tenant_id UUID NOT NULL,           -- tenant initiating the request (for callback routing)
    provider VARCHAR(50) NOT NULL,     -- 'cognito' or 'entra' (for logging)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL    -- cleanup: delete after this time
);
CREATE INDEX IF NOT EXISTS idx_saml_requests_expires ON saml_requests(expires_at);
```

**Lifecycle:** `SamlInitiate` inserts → `SamlACS` validates + deletes → cron/cleanup removes expired rows (age > 1h).

---

## Error Handling Strategy

### HTTP Status Codes
- **400 Bad Request:** Malformed SAML (XML parse error, missing required fields, assertion expired, signature failure)
- **401 Unauthorized:** Invalid token (JWT validation failures) on logout
- **403 Forbidden:** Subject mismatch (sso_subject differs from stored; possible account compromise or redirect attack)
- **404 Not Found:** SAML config not found for tenant/provider (or cross-tenant access attempt)
- **409 Conflict:** Email already linked to different tenant (see Risks)
- **503 Service Unavailable:** SecretEncryptionKey not configured (can't store certificate); IdP metadata unreachable
- **500 Internal Server Error:** Unexpected database errors, JWT generation failure

### Security Event Logging (Every Error Path)
- `saml_signature_invalid` — IdP certificate validation failed (potential attack)
- `saml_assertion_expired` — user took too long to authenticate
- `saml_assertion_malformed` — XML parse error, missing fields
- `saml_subject_mismatch` — sso_subject differs (account compromise risk)
- `saml_config_not_found` — tenant/provider mismatch (possible IDOR or misconfiguration)
- `saml_identity_link_conflict` — email already linked to different tenant

### Client Error Messages (User-Friendly)
- **Signature invalid:** "Authentication failed. Your identity provider's certificate could not be verified. Please contact your administrator."
- **Assertion expired:** "Authentication took too long. Please sign in again."
- **Malformed assertion:** "Authentication failed. Your identity provider sent an invalid response. Please try again or contact support."
- **Email already linked:** "This email is already linked to a different organization. Please contact your administrator."

---

## Testing Strategy

### Unit Tests (No DB, No HTTP)
- `saml/saml_test.go` (50+ table-driven cases):
  - Valid SAML Response parsing (Cognito + Entra fixtures)
  - Invalid signatures (tampered assertion)
  - Expired assertions
  - Malformed XML
  - Missing required fields (Email, Subject, SessionIndex)
  - Certificate validation (valid, self-signed, wrong cert)
  - AuthnRequest building (correct ID, timestamp, binding)
  - LogoutRequest building

### Integration Tests (HTTP, Mock IdP)
- `controllers/saml_auth_test.go`:
  - `/api/auth/saml/{provider}/initiate` → redirect to IdP
  - `/api/auth/saml/{provider}/acs` with valid assertion → JWT + cookie
  - `/api/auth/saml/{provider}/acs` with invalid signature → 400
  - `/api/auth/saml/{provider}/acs` with expired assertion → 400
  - `/api/auth/saml/{provider}/acs` → new identity created
  - `/api/auth/saml/{provider}/acs` (repeat) → same identity, no duplicate
  - `/api/auth/saml/{provider}/metadata` → valid SP metadata XML
  - `/api/auth/saml/{provider}/logout` (with JWT) → SLO initiated
  - Rate limiting on public endpoints (IP-based)
  - Multi-tenant isolation (tenant A config not accessible to tenant B)

### DB Tests (`-tags dbtest`)
- SAML config CRUD (create, read, update, delete)
- Identity linking (new, update existing, email case-insensitivity)
- Metadata refresh (fetch & store, cache invalidation)
- State tracking (insert, validate, cleanup)

### End-to-End Tests (Real AWS Cognito + Entra ID)
**Manual testing with real IdP configurations (not automated CI).**

#### AWS Cognito Setup
1. Create Cognito User Pool (or use existing test pool)
2. Create SAML App Integration:
   - SAML Metadata Document URL: `https://staging.stonesuite.io/api/tenant/saml/cognito/metadata`
   - ACS URL: `https://staging.stonesuite.io/api/auth/saml/cognito/acs`
   - Subject: Use email
3. Configure test user with email
4. Create SSO config in StoneSuite: `POST /api/tenant/sso-configs` with `{protocol: 'saml', provider: 'cognito', metadata_url: '<cognito-metadata-url>'}`
5. Test login flow:
   - Click "Sign in with Cognito"
   - Browser redirects to `/api/auth/saml/cognito/initiate`
   - Redirected to Cognito
   - Sign in with test user
   - Redirected back to `/api/auth/saml/cognito/acs`
   - Redirected to dashboard with JWT
   - Verify JWT contains `tenant_id`, `identity_id`, `email`
6. Test repeat login (should reuse identity, no duplicate created)
7. Test logout: JWT expires, dashboard redirects to login
8. Test invalid assertion (simulate tampered XML): verify 400 error

#### Microsoft Entra ID Setup
1. Create Entra ID Tenant (or use existing test tenant)
2. Create Enterprise Application (SAML):
   - Identifier (Entity ID): `https://staging.stonesuite.io/saml/entra/metadata`
   - Reply URL (Assertion Consumer Service URL): `https://staging.stonesuite.io/api/auth/saml/entra/acs`
   - User Email: claim source
   - Download SAML signing certificate
3. Create test user with email
4. Create SSO config: `POST /api/tenant/sso-configs` with `{protocol: 'saml', provider: 'entra', metadata_url: '<entra-metadata-url>'}`
5. Repeat login flow tests (same as Cognito)
6. Test SLO (logout): verify redirect to Entra logout endpoint

#### Expected Results
- [PASS] User created on first login with email from SAML assertion
- [PASS] User linked on repeat login (same identity, no duplicate)
- [PASS] JWT contains correct tenant_id, identity_id, email
- [PASS] Repeat login (different browser/incognito) works (stateless)
- [PASS] Invalid signatures rejected (400)
- [PASS] Expired assertions rejected (400)
- [PASS] Multi-tenant isolation: user from tenant A can't access tenant B's config
- [PASS] User can switch tenants if registered in multiple (different email or separate identity per tenant)
- [PASS] Audit logs capture all login events

#### Test Evidence Documentation
- Screenshots of successful login flow (email, assertion, JWT)
- Error logs for invalid signature test
- Database records showing identity creation + sso_subject linking
- Audit log entries showing security events
- Performance metrics (AuthnRequest build time, assertion parsing time, signature validation time)

---

## Configuration & Environment Variables

### Required (for SAML to work)
- `SECRET_ENCRYPTION_KEY` (already required for existing SSO secrets) — used to encrypt IdP certificate PEM
- At deployment time: `SAML_SP_ENTITY_ID` (e.g., `https://app.stonesuite.io/saml`) — used in metadata, AuthnRequest

### Optional (for fine-tuning)
- `SAML_REQUEST_TIMEOUT` (default: `10m`) — assertion expiry tolerance
- `SAML_METADATA_REFRESH_INTERVAL` (default: `24h`) — how often to re-fetch IdP metadata
- `SAML_REQUEST_STATE_TTL` (default: `15m`) — how long SAML request state is valid

### Example `.env`
```bash
# Existing
SECRET_ENCRYPTION_KEY="base64-encoded-32-byte-key"
JWT_SECRET="your-jwt-secret"
CONTROL_PLANE_DB_URL="postgresql://..."

# New (SAML-specific, optional at deployment)
SAML_SP_ENTITY_ID="https://app.stonesuite.io/saml"
SAML_REQUEST_TIMEOUT="10m"
SAML_METADATA_REFRESH_INTERVAL="24h"
```

### No Secrets in Config
- IdP certificates and metadata URLs are stored in the database (encrypted), not env vars
- Tenants configure them via admin UI
- No API keys or credentials required in `.env`

---

## Risks & Mitigation

### Risk 1: Tenant Resolution in SAML Flow (CRITICAL)
**Problem:** SAML assertions are provider-specific (subject ID is specific to Cognito or Entra), but don't inherently carry tenant context. On ACS callback, the backend must know which tenant the assertion belongs to — but how?

**Options:**
A. **URL path parameter:** `/api/auth/saml/{provider}?tenant_id={uuid}` or `/api/auth/saml/{tenant_slug}/{provider}` — tenant in URL, frontend provides it
B. **RelayState (query param):** AuthnRequest includes `RelayState=tenant:{uuid}`, IdP echoes it back in Response
C. **Email-domain lookup:** Assume email domain implies tenant (e.g., name@acme.com → tenant "Acme"). Breaks if multiple tenants share domain.
D. **Database lookup:** Query which tenants have SAML config for this provider, assume single tenant per provider per IdP metadata URL. Breaks if 2+ tenants use same Cognito pool.

**Recommendation:** **Option B (RelayState)** — stateless, secure, standard SAML practice. Frontend initiates login with tenant context, provides RelayState, ACS validates relay state on callback to resolve tenant. Fallback: option A if RelayState fails (some old IdPs may not echo it).

**Implementation:**
1. Frontend: when clicking "Sign in with SAML", pass `?tenant_id={uuid}` to initiate endpoint
2. Initiate endpoint: build AuthnRequest with `RelayState=tenant:{uuid}` (encrypted to prevent tampering)
3. IdP redirects to ACS with SAML Response + RelayState
4. ACS: decrypt RelayState, extract tenant_id, validate it matches stored SAML request state

### Risk 2: Email Already Linked to Different Tenant
**Problem:** User has two email addresses; one is linked to tenant A in Cognito, the other to tenant B. On login to tenant B via SAML, backend queries identities by email, finds the tenant A identity, tries to link — but tenant A identity is already linked to a different sso_subject (or vice versa).

**Mitigation:**
- On identity lookup, check if `sso_provider` is already set to a different value (e.g., old subject ID differs from new assertion)
- Return 409 Conflict: "Email already linked to different identity provider. Please use your original provider or contact support."
- Log security event: `saml_identity_link_conflict`

**Longer term:** Frontend could detect this and offer "link this email to a new account" flow, but for MVP, reject gracefully.

### Risk 3: Certificate Expiry
**Problem:** IdP certificate expires; backend can't verify new assertions. Assertions fail validation.

**Mitigation:**
- Fetch and cache IdP metadata on SAML config create/update (extract certificate, store with `metadata_fetched_at`)
- Implement metadata refresh job: cron job runs hourly, re-fetches metadata for enabled SAML configs, updates certificate if changed
- Alert if certificate expires within 30 days (log warning on validation failure, metric)
- Tenant admin is responsible for keeping metadata URL accessible and certificates current

**Implementation:** Background job (goroutine with context cancellation, part of provisioning worker pool or separate) runs hourly, fetches metadata, updates DB.

### Risk 4: XML Signature Bypass (Security)
**Problem:** gosaml2 or underlying XML-dsig library has a bug; attacker crafts assertion with fake signature that bypasses validation.

**Mitigation:**
- Use well-maintained library (`russellhaering/gosaml2`, actively maintained, used by large orgs)
- Validate certificate chain (don't just trust certificate; verify issuer)
- Unit tests with intentionally invalid signatures (tampered XML, wrong cert, self-signed, expired cert)
- Security logging: `saml_signature_invalid` event
- Monitoring/alerting on signature failures (SOC can investigate)

### Risk 5: Assertion Replay (Security)
**Problem:** Attacker captures a valid assertion from one browser session, replays it in another (different browser, IP, etc.). ACS accepts it again, logs in attacker.

**Mitigation:**
- SAML spec requires assertion to include `NotOnOrAfter` (expiry); gosaml2 validates this
- ACS tracks used assertion IDs (in DB or in-memory cache) and rejects replays (future work; not MVP)
- Single-use state: SAML request ID is single-use (stored, validated, deleted on ACS)
- Current MVP: rely on assertion expiry (typically 5-10 min); replay window is narrow

### Risk 6: CSRF on Initiate Endpoint
**Problem:** Attacker lures user to `/api/auth/saml/cognito/initiate?tenant_id=ATTACKER_TENANT`, user signs in with attacker's IdP, attacker gains access to user's identity.

**Mitigation:**
- SAML initiate endpoint requires tenant_id in request (from frontend)
- Frontend always knows which tenant user is trying to access (same-origin)
- State validation on ACS: RelayState is validated, encrypted; can't be forged without SECRET_ENCRYPTION_KEY
- Multi-tenant isolation: attacker can't use their tenant config to sign into victim's tenant

### Risk 7: Multi-Tenant Identity Linking Confusion
**Problem:** User registered in 2 tenants with same email. Both tenants use SAML. User signs in to tenant A, identity is linked to SAML subject A. Later signs in to tenant B, assertion from IdP has subject B. Backend tries to link — but identity already has sso_subject=A (different), so mismatch.

**Mitigation:**
- On SAML ACS, query identity by email + tenant_id (not just email)
- Design: every tenant-level identity lives in control-plane and belongs to exactly one tenant (tenant_id is PK)
- Behavior: same email can exist in different tenants, but each is a separate identity row with separate sso_subject linkage
- Implementation: `IdentityByEmail()` returns one global identity; query should include tenant_id filter to get the right one

Actually, **this is a design decision:** should email be globally unique across all tenants, or scoped per-tenant?

**Current schema:** `identities.email` has a UNIQUE constraint (global), so email is globally unique. This means:
- User with email `alice@example.com` can only exist once in control-plane
- If they register with tenant A and then try to register with tenant B using same email, they get an error or auto-join tenant B

**For SAML:** this simplifies things. On ACS, query by email returns exactly one identity. That identity belongs to a specific tenant. If the assertion tenant_id doesn't match the identity's tenant, reject with 403.

**Risk mitigation:** Explicitly document this design; update schema comment if needed.

### Risk 8: IdP Metadata Unreachable at Setup Time
**Problem:** Tenant admin enters metadata URL, backend tries to fetch it but network is down, URL is wrong, or IdP is unreachable.

**Mitigation:**
- Fetch metadata on config create/update; if fetch fails, return 503 with message
- Tenant admin sees error immediately, can retry or check URL
- Store fetched metadata + timestamp; if fetch fails and we have cached metadata older than 1 day, warn but allow (graceful degradation)
- Document required proxy/firewall rules for outbound HTTPS to IdP metadata endpoints

### Risk 9: Performance (Metadata Fetch on Create)
**Problem:** Creating/updating SAML config is synchronous; fetching IdP metadata (network call) blocks the request.

**Mitigation:**
- Fetch metadata synchronously on create (one-time, user expects a delay)
- On update: if metadata_url hasn't changed, don't re-fetch (skip network call)
- Background job refreshes metadata hourly (async)
- If fetch takes >5s, timeout and return error (user can retry)

### Risk 10: Log Injection (Security)
**Problem:** SAML assertion contains user-controlled data (email, name, attributes). If we log these directly, attacker could craft assertion with newlines/quotes to inject fake log lines.

**Mitigation:**
- Use structured logging (`slog`); never interpolate strings
- Log only `slog.String()` or `slog.Any()` with automatic JSON encoding
- Current code pattern: `logSecurityEvent(r, "event_name", "email", email, ...)` — slog handles escaping
- Existing code already safe; no additional work

---

## Security Checklist

- [ ] XML signature validation using gosaml2 (no custom DSig logic)
- [ ] Certificate validation: verify issuer, expiry, chain (gosaml2 handles; verify config)
- [ ] Assertion expiry validation (NotOnOrAfter) mandatory
- [ ] CSRF state tracking (SAML request ID storage + validation)
- [ ] Email case-insensitive lookup (existing code: `LOWER(email)`)
- [ ] Tenant-scoped identity queries (WHERE tenant_id in all queries)
- [ ] IDOR guard: deny access to cross-tenant SAML configs (existing code: GetSSOConfig scoped by tenant)
- [ ] Security logging for all auth paths (success + failures)
- [ ] No secrets in logs (password, assertion, certificate, JWT)
- [ ] Rate limiting on public endpoints (reuse existing IP-based limiter)
- [ ] TLS enforcement for IdP metadata fetch (only https://)
- [ ] Secret encryption at rest (certificate_pem, client_secret already encrypted)

---

## Rollout Plan

### Phase 1: Internal Testing (1-2 weeks)
- Deploy to staging with real AWS Cognito test pool + Entra ID test tenant
- Run manual end-to-end tests (login, logout, error paths)
- Load testing (assertion parsing speed, database load)
- Security review (audit logs, error messages, no leaks)

### Phase 2: Beta Customer (1-2 weeks)
- Enable SAML for single trusted customer (e.g., internal demo tenant)
- Collect feedback on UX, error messages, certificate management
- Monitor logs for unexpected errors

### Phase 3: General Availability (Rollout)
- Enable SAML feature flag (optional at deployment time)
- Document for customers (setup guide, troubleshooting)
- Announce in release notes
- Monitor production for certificate expiry, signature failures, performance

### Rollback Plan
- SAML config is per-tenant, opt-in
- If SAML breaks, customers fall back to password login or existing OIDC
- If gosaml2 library has critical bug, revert dependency change
- No schema changes affect existing OIDC configs (backward compatible)

---

## Success Criteria

1. ✅ SAML 2.0 assertions (from AWS Cognito and Microsoft Entra ID) successfully parsed and validated
2. ✅ New identities created on first SAML login (email extracted, sso_subject linked)
3. ✅ Repeat SAML logins reuse identity (no duplicates)
4. ✅ JWT minted with tenant_id, identity_id, email (same as password login)
5. ✅ Invalid signatures rejected with 400 + security log
6. ✅ Expired assertions rejected with 400 + security log
7. ✅ Multi-tenant isolation verified (tenant A config can't sign in user for tenant B)
8. ✅ Audit logs capture all SAML events
9. ✅ RBAC permissions enforced (sso_config read/configure actions)
10. ✅ Full test coverage (unit, integration, db-backed, end-to-end manual)
11. ✅ Environment variables documented (no secrets in config)
12. ✅ Frontend integration documented (setup guides for AWS + Entra)
13. ✅ Build succeeds, tests pass locally and in CI

---

## Appendix: Reference SAML Flow Diagram

```
┌─────────────────┐                                    ┌──────────────────┐
│    Frontend     │                                    │      IdP         │
│  (StoneSuite)   │                                    │  (Cognito/Entra) │
└────────┬────────┘                                    └────────┬─────────┘
         │                                                       │
         │ 1. Click "Sign in with [Provider]"                  │
         │ Query: ?tenant_id=UUID                              │
         ├─────────────────────────────────────────────┟        │
         │          /api/auth/saml/{provider}/initiate          │
         │                                             │        │
         │◄─────────────────────────────────────────────        │
         │  2. HTTP 302 Redirect                       │        │
         │  Location: https://idp.example.com/sso/... │        │
         │  Body: SAMLRequest (AuthnRequest, signed)   │        │
         │                    RelayState=tenant:UUID   │        │
         │                                                      │
         │  3. Browser follows redirect ──────────────────────► │
         │                                                       │
         │                                      4. User authenticates
         │                                         (username/password)
         │                                                       │
         │                                    5. IdP builds SAMLResponse
         │                                       (signed assertion)
         │                                                       │
         │◄──────────────────────────────────────────────────── │
         │  6. Browser POSTs to /api/auth/saml/{provider}/acs   │
         │  Body: SAMLResponse, RelayState                      │
         │                                                       │
         │  7. Backend:                                         │
         │     - Validates signature (cert from IdP metadata)   │
         │     - Extracts email, subject, sessionIndex          │
         │     - Links/creates identity                         │
         │     - Generates JWT                                  │
         │                                                       │
         │◄─────────────────────────────────────────────────────┤
         │  8. HTTP 302 Redirect to /dashboard                  │
         │  Set-Cookie: auth_token=JWT                          │
         │                                                       │
         │  9. Browser loads dashboard with JWT                 │
         │     All requests include Authorization: Bearer JWT   │
         │
```

---

## Document Review Checklist

- [x] Architecture clearly described (components, flow)
- [x] All steps listed (sequential order specified)
- [x] Files affected identified (new + modified)
- [x] Dependencies specified (gosaml2 + transitive)
- [x] Database changes idempotent (ALTER TABLE IF NOT EXISTS)
- [x] Security risks identified + mitigated
- [x] Testing strategy detailed (unit, integration, e2e, real IdP)
- [x] Configuration strategy clear (env vars, no secrets in config)
- [x] Error handling comprehensive (HTTP codes, security logging)
- [x] Rollout plan includes fallback
- [x] Success criteria defined
- [x] Multi-tenancy respected (tenant scoping in all queries)
- [x] RBAC integration (existing `sso_config` resource reused)
- [x] No breaking changes to existing SSO (backward compatible)

---

## Questions for User Approval

Before implementation, please confirm:

1. **Tenant Resolution:** Does RelayState-based tenant identification (Option B above) work for your use case? Or prefer URL parameter (Option A)?

2. **Create-on-First-Login vs Pre-Provisioning:** Should SAML login automatically create a new identity if email doesn't exist? Or require admin pre-provisioning? (Recommendation: create-on-first-login for better UX, with email verification already set to true since IdP verified it)

3. **Email Uniqueness:** Is the global email uniqueness constraint acceptable? (Current design: email is globally unique across all tenants; same email can't be registered in multiple tenants independently.)

4. **Multi-Provider per Tenant:** Can one tenant configure SAML for multiple providers (e.g., Cognito + Entra)? (Yes, current design allows one SAML config per provider per tenant; no limit beyond that.)

5. **Certificate Handling:** Should IdP certificate be fetched from metadata URL automatically? Or allow manual upload/paste? (Recommendation: auto-fetch from metadata URL; supports manual override later if needed)

6. **Logout Implementation:** Should SAML SLO (Single Logout) be best-effort or required? (Recommendation: best-effort; if SLO fails, JWT expiry is sufficient)

7. **Test Scope:** For end-to-end testing, can you provide access to test AWS Cognito pool + Entra ID test tenant? Or should I use public test instances?

---

**Next Step:** User reviews this plan, provides answers to the questions above, and approves (or requests changes). Once approved, implementation proceeds step-by-step with go-implementer agent dispatches.

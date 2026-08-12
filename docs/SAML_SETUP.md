# SAML 2.0 SSO Setup Guide

Operator/developer reference for StoneSuite's SAML 2.0 single sign-on
implementation. Every route, field name, default, and error message below was
read directly from the code that ships it (see the file pointers throughout);
none of it is aspirational.

## Contents
1. [Overview](#1-overview)
2. [Architecture summary](#2-architecture-summary)
3. [Required environment variables](#3-required-environment-variables)
4. [AWS Cognito setup walkthrough](#4-aws-cognito-setup-walkthrough)
5. [Microsoft Entra ID setup walkthrough](#5-microsoft-entra-id-setup-walkthrough)
6. [API reference](#6-api-reference)
7. [Identity linking & provisioning behavior](#7-identity-linking--provisioning-behavior)
8. [Known limitations](#8-known-limitations)
9. [Manual end-to-end testing guide](#9-manual-end-to-end-testing-guide)
10. [Troubleshooting](#10-troubleshooting)

---

## 1. Overview

This implements **SP-initiated SAML 2.0 single sign-on** for two identity
providers — **AWS Cognito** and **Microsoft Entra ID** — configured
independently per tenant. It covers:

- Per-tenant SAML configuration (IdP metadata URL, certificate, SSO/SLO URLs)
  stored alongside the pre-existing OIDC config rows, in the same
  `tenant_sso_configs` table (`protocol = 'saml'`).
- The full login mechanics: AuthnRequest generation, redirect to the IdP,
  assertion consumption (signature + expiry + audience validation), and
  minting a real StoneSuite session (JWT + cookies) at the end.
- **JIT (just-in-time) user provisioning**: a first-time SAML login creates
  both a control-plane identity and a tenant-local `users` row automatically,
  matched/linked by email.
- SP-initiated logout (best-effort): if the IdP advertises a Single Logout
  endpoint, StoneSuite builds and returns a `LogoutRequest` URL for the
  frontend to send the browser to.

**What this explicitly does NOT implement:**

- **IdP-initiated login.** There is no endpoint that accepts an unsolicited
  assertion. A login must start at StoneSuite's own
  `GET /api/auth/saml/{provider}/initiate`. Clicking an app tile in the IdP's
  own portal (e.g. Entra's "My Apps") will not work.
- **IdP-initiated SLO.** Nothing consumes an unsolicited `LogoutRequest` sent
  by the IdP (e.g. an admin forcing a session end on the IdP side does not
  propagate to StoneSuite).
- **Automatic/background certificate or metadata refresh.** IdP metadata
  (including the signing certificate) is fetched once at config create/update
  time and otherwise never changes on its own — there is no scheduled job.
  Refreshing it is a manual, admin-triggered API call
  (`POST /api/tenant/sso-configs/{id}/refresh-metadata`, see §6).

Read [§8 Known limitations](#8-known-limitations) before relying on this in
production — several more gaps exist beyond the three above, including one
that affects every SP-initiated logout (§8).

This is a backend-only implementation. Do not point real production IdP
integrations at it and expect a working login button without also reading the
frontend gaps called out in §8 — as of this writing there is no frontend
integration at all.

---

## 2. Architecture summary

A SAML config lives per tenant *and* per provider
(`tenant_sso_configs`, unique on `(tenant_id, provider)` — see §8 for what
that constraint implies), but the backend's SAML **endpoints are shared per
provider, not per tenant**: every tenant that configures Cognito SAML points
their IdP at the *same* ACS URL (`{API_BASE_URL}/api/auth/saml/cognito/acs`).
There is no tenant segment anywhere in a SAML URL. Because of that, the
backend can never trust anything the browser or the IdP supplies to say
*which tenant* an inbound assertion belongs to — tenant identity has to come
entirely from state the backend itself created and controls.

**Request flow:**
1. **Initiate** (`GET /api/auth/saml/{provider}/initiate?tenant_slug=...`) —
   the backend resolves the tenant from the query param, loads that tenant's
   enabled SAML config, builds an unsigned `AuthnRequest`, records
   `{AuthnRequest ID → tenant_id, provider, expiry}` server-side
   (`saml_requests`, TTL = `SAML_REQUEST_STATE_TTL`), and 302-redirects the
   browser to the IdP's SSO URL.
2. **IdP** authenticates the user by whatever means it wants and POSTs a
   signed `SAMLResponse` back to the ACS URL.
3. **ACS** (`POST /api/auth/saml/{provider}/acs`) — decodes the response
   *unverified* far enough to read only `InResponseTo` (an opaque id it never
   trusts for anything else), looks that id up in `saml_requests`
   (single-use — the row is deleted atomically on read), and only *then*
   knows which tenant's stored certificate to validate the signature against.
   Only after signature + expiry + audience validation succeeds does it
   extract the email, link-or-create the identity/user, and mint a short-lived
   **login code**.
4. ACS redirects the browser to
   `{FRONTEND_URL}/auth/sso/callback?code=...` — never a JWT in the URL.
5. **Exchange** (`POST /api/auth/saml/exchange`) — the frontend immediately
   trades that one-time code for the real JWT + `auth_token`/`refresh_token`
   cookies, same envelope shape as password login.

**Why a login code instead of a token in the URL:** a JWT in a redirect's
query string is recoverable from browser history and gets replayed verbatim
in the `Referer` header of any subsequent cross-origin request from that
landing page — either leak is equivalent to handing out a live session. The
code ACS mints instead (`saml_login_codes`, 64 hex chars, 60s TTL, single-use)
is worthless outside of an immediate server-side exchange.

**Why tenant resolution uses `InResponseTo` correlation, not anything
client-supplied:** the original design draft
(`docs/superpowers/plans/2026-08-07-saml-sso-backend.md`) proposed carrying
an encrypted tenant id in SAML's `RelayState`. What actually shipped instead
correlates purely against the server-generated `saml_requests` row keyed by
the AuthnRequest's own `ID` (which SAML requires the IdP to echo back as the
response's `InResponseTo`). `RelayState` in the shipped code carries only a
UI redirect hint (`return_to`, e.g. "send the user back to this deep link
after login") and is **never** read for any trust or identity decision —
`saml_auth.go`'s `Initiate`/`ACS` doc comments call this out explicitly. This
matters because `RelayState` travels as a separate, unsigned query/form
parameter in the HTTP-POST/Redirect bindings — it is not covered by the SAML
response's own XML signature — so it must never be the thing that decides
which tenant's certificate a signature gets checked against.

Built on `github.com/russellhaering/gosaml2` v0.12.0 +
`github.com/russellhaering/goxmldsig` v1.6.1 (XML-dsig) +
`github.com/beevik/etree` v1.7.0 (XML). AuthnRequests are never signed
(`SignAuthnRequests: false` in `saml/saml.go`) — neither Cognito nor Entra
requires it — but response signature validation is never skipped.

---

## 3. Required environment variables

All read via `config.AppConfig` (`config/config.go`); none are hardcoded
anywhere in the SAML code.

### New, SAML-specific

| Variable | Purpose | Default | Required? |
|---|---|---|---|
| `SAML_SP_ENTITY_ID` | Prefix for this SP's entity id; the actual id used per provider is `{SAML_SP_ENTITY_ID}/{provider}/metadata`. This is only an *identifier string* — it does not need to be a fetchable URL — but it **is** checked as the assertion's `Audience`, so whatever you set here must exactly match what you configure as the "Identifier / Entity ID" / "Audience URI" on the IdP side. | `https://app.stonesuite.io/saml` | Optional (has a default), but **override it** unless you are literally deploying at `app.stonesuite.io` — the default points at StoneSuite's own hosted frontend domain, not your deployment. |
| `SAML_REQUEST_STATE_TTL` | How long a server-side `saml_requests` row (the single-use AuthnRequest state ACS correlates against) stays valid. This is the real "you have N minutes to finish logging in at the IdP" budget — `ConsumeSAMLRequestState` rejects anything past it. | `15m` | Optional. |
| `SAML_REQUEST_TIMEOUT` | Documented in `config/config.go` as "assertion expiry tolerance." **Not read by any code path today** — parsed and defaulted, never consulted. Assertion expiry is validated entirely from the assertion's own `NotOnOrAfter`/`NotBefore`, via `gosaml2`. Treat this as reserved/dead config; do not expect changing it to affect anything. | `10m` | N/A — currently a no-op. |
| `SAML_METADATA_REFRESH_INTERVAL` | Documented as "IdP metadata cache duration." **Also not read by any code path.** There is no background refresh loop — see §1/§8. | `24h` | N/A — currently a no-op. |
| `API_BASE_URL` | This backend's own public base URL. Used to build the *real*, reachable ACS/metadata/SLO-response URLs (`{API_BASE_URL}/api/auth/saml/{provider}/...`) — never the frontend's. This is the exact value you give every IdP as the ACS/Reply URL. | `http://localhost:8080` | **Yes**, for any non-local deployment. Getting this wrong breaks SAML login for every tenant. |

### Existing variables SAML now also depends on

| Variable | Why SAML needs it | Notes |
|---|---|---|
| `SECRET_ENCRYPTION_KEY` | Every SAML config's IdP signing certificate is encrypted at rest (`tenant_sso_configs.certificate_pem_enc`) with the same field-level cipher already used for OIDC's `client_secret_enc` and tenant DSNs. | Without it, `SSOOps.encryptSecret` fails closed with **503** on every SAML config create/update/refresh — **no SAML config can ever be created**, the same failure mode OIDC already has for its client secret. Already mandatory in production (`config.Validate`); SAML just adds a second consumer. |
| `FRONTEND_URL` | ACS redirects the browser to `{FRONTEND_URL}/auth/sso/callback?code=...` on success. | See §8 — that route doesn't exist in the frontend yet, so this redirect currently lands on a 404 there; the backend half is still independently testable (§9). |
| `JWT_SECRET` | `POST /api/auth/saml/exchange` signs the session token with the same `generateTenantJWT` helper password/OIDC login uses. | Pre-existing requirement, unrelated to SAML specifically. |

---

## 4. AWS Cognito setup walkthrough

The three backend URLs you'll need (substitute your real `API_BASE_URL` and,
if you overrode it, `SAML_SP_ENTITY_ID`):

```
SP Entity ID (Audience) :  ${SAML_SP_ENTITY_ID}/cognito/metadata
SP metadata document    :  GET  ${API_BASE_URL}/api/auth/saml/cognito/metadata
ACS / Reply URL         :  POST ${API_BASE_URL}/api/auth/saml/cognito/acs
```

1. **Create (or reuse) a Cognito User Pool**, then set it up as a **SAML
   identity provider for this application** — i.e. Cognito issues the SAML
   assertions StoneSuite consumes. In the User Pool's app-integration/SAML
   application area, add a new SAML application (menu names have moved
   around across AWS console revisions — look for wherever your pool lets
   you register a downstream SAML relying party/application).
2. When it asks for the **relying party (SP) configuration**, provide:
   - **ACS URL / Reply URL:** `${API_BASE_URL}/api/auth/saml/cognito/acs`
   - **SP Entity ID / Audience:** `${SAML_SP_ENTITY_ID}/cognito/metadata`
   - If it offers to import SP metadata by URL instead of filling these in by
     hand, you can point it at
     `${API_BASE_URL}/api/auth/saml/cognito/metadata` — that document
     declares the ACS URL (HTTP-POST binding) and entity id above, and
     nothing else (no SP signing certificate; StoneSuite never signs
     AuthnRequests, see §2).
   - Map the **email** attribute in whatever way Cognito calls it in that
     screen (subject/NameID, or a `email`/`mail`-style attribute) — StoneSuite
     accepts either (see §7/`saml/response.go`).
3. Finish creating the application. Cognito will show you **its own SAML
   metadata document URL** (or a downloadable metadata XML) for this
   application — this is the IdP metadata StoneSuite needs. Copy that URL
   verbatim; it must be reachable over plain HTTPS with no auth, since the
   backend fetches it directly (`saml.FetchIdPMetadata` enforces `https://`,
   a 10s timeout, and a 2MB response cap — `saml/metadata.go`).
4. Create a test user in the pool with a real email address.
5. Register the config in StoneSuite as a tenant admin
   (`sso_config:configure` permission required):
   ```bash
   curl -X POST "$API_BASE_URL/api/tenant/sso-configs" \
     -H "Authorization: Bearer $ADMIN_JWT" \
     -H "Content-Type: application/json" \
     -d '{
       "provider": "cognito",
       "protocol": "saml",
       "metadata_url": "<the Cognito SAML metadata URL from step 3>",
       "enabled": true
     }'
   ```
   Field names above are exactly `ssoConfigRequest` in `controllers/sso.go`.
   `client_id`/`client_secret`/`issuer`/`redirect_uri` are OIDC-only fields on
   the same struct — omit them (or leave them; they're simply ignored for
   `protocol: "saml"`, `validateSSORequest` never inspects them in that
   branch). A `201` response's `sso_config.idp_entity_id`, `.sso_url`, and
   `.certificate_fingerprint` being non-empty confirms the metadata fetch and
   parse succeeded.
6. Test end-to-end per §9.

---

## 5. Microsoft Entra ID setup walkthrough

Same three URLs, provider segment `entra`:

```
SP Entity ID (Audience) :  ${SAML_SP_ENTITY_ID}/entra/metadata
SP metadata document    :  GET  ${API_BASE_URL}/api/auth/saml/entra/metadata
ACS / Reply URL         :  POST ${API_BASE_URL}/api/auth/saml/entra/acs
```

1. In the Entra admin center: **Enterprise Applications → New application →
   Create your own application** (non-gallery), then open its **Single
   sign-on** blade and choose **SAML**.
2. Under **Basic SAML Configuration**, set:
   - **Identifier (Entity ID):** `${SAML_SP_ENTITY_ID}/entra/metadata`
   - **Reply URL (Assertion Consumer Service URL):**
     `${API_BASE_URL}/api/auth/saml/entra/acs`
   - **Sign on URL:** leave blank, or point it at StoneSuite's own sign-in
     page — do **not** rely on it to actually start a login, since Entra's
     "My Apps" tile launch is an IdP-initiated flow and this backend does not
     support IdP-initiated login (§8). Tell end users to always start at
     StoneSuite's sign-in page.
3. Under **Attributes & Claims**, Entra's default claim set already emits
   `http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress` for
   `user.mail`, plus the classic `.../givenname` and `.../surname` claims —
   these are exactly the attribute names `saml/response.go` and
   `controllers/saml_acs.go` look for (`emailAttributeCandidates`,
   `givenNameAttribute`/`surnameAttribute`), so the out-of-the-box claim
   mapping works without changes in the common case. If your tenant's **Name
   ID** (under "Unique User Identifier") isn't mapped to an email address
   (e.g. it's using an opaque object id), either remap it to `user.mail` or
   confirm the `emailaddress` claim above is present — StoneSuite falls back
   to it whenever the NameID doesn't contain `@` (`saml/response.go`'s
   `extractEmail`).
4. Under **SAML Certificates**, copy the **App Federation Metadata Url** —
   this is the IdP metadata URL you'll feed to StoneSuite. It's a public
   HTTPS URL, no auth needed to fetch it.
5. Assign a test user to the Enterprise Application (Entra requires explicit
   assignment before a user can authenticate against a non-gallery SAML app).
6. Register the config:
   ```bash
   curl -X POST "$API_BASE_URL/api/tenant/sso-configs" \
     -H "Authorization: Bearer $ADMIN_JWT" \
     -H "Content-Type: application/json" \
     -d '{
       "provider": "entra",
       "protocol": "saml",
       "metadata_url": "<App Federation Metadata Url from step 4>",
       "enabled": true
     }'
   ```
7. Test end-to-end per §9.

---

## 6. API reference

| Method | Path | Auth | Rate limit |
|---|---|---|---|
| `GET` | `/api/auth/saml/{provider}/metadata` | none | none |
| `GET` | `/api/auth/saml/{provider}/sp-info` | none | none |
| `GET` | `/api/auth/saml/{provider}/initiate` | none | per-IP (burst 10, ~12/min sustained) |
| `POST` | `/api/auth/saml/{provider}/acs` | none (IdP-facing) | per-IP (same as above) |
| `POST` | `/api/auth/saml/exchange` | none (one-time code is the credential) | per-IP (same as above) |
| `POST` | `/api/auth/saml/{provider}/logout` | JWT required (full `tenantChain`) | per-tenant (burst 40, 20 req/s) |
| `POST` | `/api/auth/saml/discover` | none | per-IP (same as above) |
| `GET`/`POST`/`PUT`/`DELETE` | `/api/tenant/sso-configs[/{id}]` | JWT + `sso_config:read`/`configure` | per-tenant |
| `POST` | `/api/tenant/sso-configs/{id}/refresh-metadata` | JWT + `sso_config:configure` | per-tenant |
| `GET`/`POST`/`DELETE` | `/api/tenant/sso-configs/{id}/domains[/{domainId}]` | JWT + `sso_config:read`/`configure` | per-tenant |

`{provider}` is `cognito` or `entra` (`samlProviders` in `controllers/sso.go`)
— anything else, including `okta`, is `404 {"success":false,"message":"Not found."}`
on every SAML route.

### `GET /api/auth/saml/{provider}/metadata`
Public, no DB call. Returns this SP's metadata document,
`Content-Type: application/samlmetadata+xml`. Declares one
`AssertionConsumerService` (HTTP-POST, index 1) at the ACS URL below, a
`SingleLogoutService` (HTTP-Redirect) if this tenant/provider has an SLO URL
configured, `AuthnRequestsSigned=false`, `WantAssertionsSigned=true`. No
`<KeyDescriptor>` — this SP has no signing certificate of its own.

### `GET /api/auth/saml/{provider}/sp-info`
Public, no DB call. Returns the same SP entity id / ACS URL / SLO URL as the
metadata document above, as JSON, for callers that don't want to parse XML:
```json
{"success": true, "provider": "entra", "sp_entity_id": "...", "acs_url": "...", "slo_url": "..."}
```

### `POST /api/auth/saml/discover`
Public, no auth, no tenant resolved yet. **Home-realm discovery**: lets the
login page resolve a user's work email to a tenant + provider instead of
asking for a workspace slug. Body:
```json
{"email": "jane@contoso.com"}
```
The email is normalized to its domain (`NormalizeEmailDomain` — lowercased,
`@`-and-everything-before-it stripped) and looked up against
`tenant_sso_domains` (registered via `POST .../sso-configs/{id}/domains`, see
[`docs/api/config-endpoints.md`](./api/config-endpoints.md)). Only an
*enabled*, `protocol=saml` config's domains can ever match — same guard as
`GetSSOConfigForAuth` used by `/initiate` below.
```json
{"success": true, "found": true, "provider": "entra", "tenant_id": "..."}
```
or
```json
{"success": true, "found": false}
```
`found: false` covers every "can't route this" case identically — malformed
email, unregistered domain, disabled config, or a config whose tenant isn't
`Servable()` — deliberately, so the response can't be used to fingerprint
*why* it failed (industry-standard for HRD endpoints; a mild, accepted
information leak, not a security boundary). The returned `tenant_id` is fed
straight into `/initiate?tenant_id=...` below — nothing new is exposed beyond
what a distributed sign-in link already carries (§6, `sp-info`).
Deliberately `POST`, not `GET`: an email in a query string ends up in server
logs and `Referer` headers.

### `GET /api/auth/saml/{provider}/initiate?tenant_slug=...|tenant_id=...&return_to=...`
Exactly one of `tenant_slug`/`tenant_id` is required (400 otherwise). On
success, `302` redirect to the IdP's SSO URL with an unsigned `AuthnRequest`;
`return_to` (if present) becomes `RelayState` (a UI hint only, see §2).
Errors: `400` missing/both tenant params; `404` unknown tenant, or no
*enabled* `protocol=saml` config for that tenant+provider; `403` tenant
exists but isn't servable (`tenant.Servable()` — active, migrated, has a DB);
`500` misc (including "SAML sign-in is not available." if the server has no
secret cipher configured at all).

### `POST /api/auth/saml/{provider}/acs`
IdP posts here, `application/x-www-form-urlencoded`, field `SAMLResponse`
(base64), optional `RelayState`. **Every failure renders the same static
HTML error page** (`samlErrorPageHTML` — never JSON, never any
request/assertion-derived text) with a status code from `{400, 403, 500}`;
the actual reason is written only to the structured security log via
`logSecurityEvent`, one of a fixed set of event names
(`saml_acs_missing_response`, `saml_acs_malformed_response`,
`saml_request_state_invalid`, `saml_acs_tenant_unservable`,
`saml_acs_config_not_found`, `saml_acs_cert_decrypt_failed`,
`saml_signature_invalid`, `saml_identity_tenant_mismatch`,
`saml_login_blocked_suspended_user`, `saml_login_code_failed`, ...). Note
`saml_signature_invalid` is logged for *every* `ParseAndValidateResponse`
failure — an actually-bad signature, an expired assertion, or an
audience/tenant mismatch all share that event name; the differentiating
detail is only in the log line's `error` field (see §10). On success: `302`
to `{FRONTEND_URL}/auth/sso/callback?code=...[&return_to=...]`.

### `POST /api/auth/saml/exchange`
Body: `{"code": "<from the ACS redirect>"}`. `400` if missing/invalid/expired
(single-use, 60s TTL). On success, `200`:
```json
{
  "success": true,
  "token": "<jwt>",
  "expiresAt": 1755000000000,
  "user": {
    "id": "identity-uuid", "email": "user@example.com",
    "fullName": "Ada Lovelace", "tenantId": "tenant-uuid",
    "isPlatformAdmin": false
  }
}
```
Also sets `auth_token` (httpOnly, `Path=/`) and, best-effort,
`refresh_token` (httpOnly, `Path=/api/auth`) cookies — identical shape/cookie
names to password login (`controllers/saml_exchange.go`).

### `POST /api/auth/saml/{provider}/logout`
Requires a valid session (JWT via header or `auth_token` cookie). Clears
local `auth_token`/`refresh_token` cookies unconditionally, *before* anything
SLO-related is attempted. `400` if the current session wasn't established via
this provider. Otherwise always `200`:
```json
{"success": true, "slo_available": false}
```
or, if the IdP advertises SLO and everything needed to build the request is
available:
```json
{"success": true, "slo_available": true, "logout_url": "https://idp.../slo?SAMLRequest=..."}
```
The frontend is expected to navigate the browser to `logout_url` next. See
§8 for what happens when the IdP redirects back.

### `/api/tenant/sso-configs...` (config CRUD + domains)
Covered in full, including the SAML-specific request/response fields, in
[`docs/api/config-endpoints.md`](./api/config-endpoints.md). Summary:
`GET`/`POST`/`PUT`/`DELETE` as usual; `POST .../{id}/refresh-metadata`
re-fetches the stored `metadata_url` and updates the IdP entity id, SSO/SLO
URLs, certificate, and fingerprint — `400` if the target config isn't
`protocol=saml`; `GET`/`POST`/`DELETE .../{id}/domains[/{domainId}]` manages
the email domains `/discover` (above) matches against.

---

## 7. Identity linking & provisioning behavior

**Email is the link key.** On a successful assertion, ACS looks up
`identities` by `LOWER(email)` — globally, not scoped to a tenant, since
`identities.email` has a global `UNIQUE` constraint
(`database/migrations/control_plane/schema.sql`). If an identity with that
email exists but belongs to a *different* tenant than the one this login was
initiated against, the request is rejected (`403`,
`saml_identity_tenant_mismatch` logged) rather than silently logging the user
into the wrong tenant or merging accounts.

**First-time login JIT-provisions two rows:**
1. A control-plane `identities` row (`tenancy.CreateSSOIdentity`) —
   `email_verified = true` (the IdP already vouched for the address), no
   password hash, `sso_provider`/`sso_subject` set from the assertion.
2. A tenant-database `users` row (`userstore.CreateUser`) — `status = 'active'`.

**The new user gets NO role assigned, unless the config sets one.**
`CreateUser` inserts a bare `users` row with zero entries in `user_roles`; if
the matched `tenant_sso_configs` row has `default_role_id` set (opt-in, an
admin picks it explicitly when configuring the provider — see
[`docs/api/config-endpoints.md`](./api/config-endpoints.md)), `saml_acs.go`
then calls `authz.AssignRole` for that role immediately after. Either way, the
identity can authenticate (it gets a valid JWT from Exchange); with no
default role, every permission check on every tenant endpoint denies it until
a tenant admin explicitly grants one:
```bash
curl -X POST "$API_BASE_URL/api/tenant/users/$USER_ID/roles" \
  -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" \
  -d '{"roleId": "<role-uuid>"}'
```
(`controllers/rbac.go`'s `UserRoles`, requires `role:update`.)

**Unset (`default_role_id` empty) is still the out-of-the-box default, and
remains the conservative choice, not an oversight:** RBAC in this codebase is
fail-closed by design, and an IdP asserting an email address is proof of
*authentication* (who this person is), not *authorization* (what they're
allowed to do here). Auto-granting a role based purely on a successful SSO
login means anyone your IdP will authenticate gets that level of access with
no human review at signup time — `default_role_id` exists for tenants that
want that tradeoff deliberately (e.g. every `@contoso.com` employee should
land in the app as a baseline "Member," not locked out), gated behind its own
`role:update` permission check and a block on system roles
(`validateDefaultRoleID`, `controllers/sso.go`) so `sso_config:configure`
alone can't be used to self-grant `super_admin` via a side channel. A role
assignment failure at login time is non-fatal — the user still gets a working
session, just with no role (the original behavior), logged server-side as
`saml_default_role_assign_failed` for an admin to fix manually.

**Repeat logins** skip provisioning: the existing identity is looked up,
`LinkSSOIdentity` refreshes `sso_provider`/`sso_subject`/`sso_session_index`
(idempotent), and the existing tenant `users` row (looked up by
`identity_id`) is reused as-is — including whatever roles it already has and
whatever `status` it's in (a `suspended`/`disabled` user is rejected with
`403`, `saml_login_blocked_suspended_user`).

---

## 8. Known limitations

- **No IdP-initiated login.** SP-initiated only; see §1.
- **No IdP-initiated SLO.** See §1.
- **No automatic metadata/certificate refresh.** `SAML_METADATA_REFRESH_INTERVAL`
  is parsed from the environment but read by nothing (verified: no reference
  to `config.AppConfig.SAMLMetadataRefreshInterval` anywhere outside
  `config.go`). The only ways stored IdP metadata/certificate ever change
  after config creation are a full `PUT` update (always re-fetches) or
  `POST .../refresh-metadata`. If an IdP rotates its signing certificate,
  every login for that tenant+provider starts failing signature validation
  (§10) until an admin — or an external cron hitting the refresh endpoint —
  updates it.
- **SP-initiated logout's return leg does not validate the IdP's response.**
  `GET /api/auth/saml/{provider}/logout-response` (the URL advertised in SP
  metadata's `SingleLogoutServices` entry and as `ServiceProviderSLOURL` in
  the `LogoutRequest`) exists and redirects the browser to
  `{FRONTEND_URL}/auth/login?logged_out=true`, but it does not parse or
  verify the IdP's `LogoutResponse` — it redirects unconditionally regardless
  of what the IdP sends back. This is safe (local StoneSuite cookies are
  already cleared before the browser is ever sent to the IdP, §6, so there is
  no session left to protect on the way back), but it means a `LogoutResponse`
  indicating the IdP-side logout actually failed is not surfaced anywhere.
- **First-login-then-immediate-logout sends an empty `SessionIndex`.**
  `CreateSSOIdentity` (the JIT-provisioning path in `saml_acs.go`) never sets
  `sso_session_index` — only `LinkSSOIdentity` (the *repeat*-login path) sets
  it. A user's very first SAML login followed immediately by
  `POST .../logout`, with no second login in between, has
  `identity.SSOSessionIndex == ""`; `Logout` passes that empty string
  straight through to `saml.BuildLogoutRequestURL` as `sessionIndex`. The
  request is still built and sent, but most IdPs will not be able to match it
  to a real session.
- **Frontend integration is now wired** (StoneSuite-WebUI):
  `/auth/sso/callback` exchanges the code and completes login; the login page
  accepts a `tenant_id`/`tenant_slug` deep link, or lets the user pick a
  provider and type their work email (home-realm discovery via
  `POST /api/auth/saml/discover`, §6) instead of a workspace slug;
  Configuration → SAML Setup drives the full config CRUD + refresh-metadata +
  domain management + default-role selection against the real API, including
  the SP entity id/ACS URL via `GET /api/auth/saml/{provider}/sp-info` (§6).
  Not yet verified against a live IdP end-to-end — that still needs a real
  Entra or Cognito tenant.
- **Domain registration for discovery is not ownership-verified.** Any tenant
  admin with `sso_config:configure` can register any domain not on the
  public-provider blocklist (`controllers/sso.go`'s
  `publicEmailDomainBlocklist`) against their own config — including a real
  third party's corporate domain. `tenant_sso_domains.verified_at` is reserved
  for a future DNS-TXT verification flow; nothing enforces it yet. See
  [`docs/api/config-endpoints.md`](./api/config-endpoints.md)'s domains
  section for the full note.
- **Okta is not supported for SAML.** `samlProviders` (`controllers/sso.go`)
  only whitelists `entra` and `cognito`; `{"protocol":"saml","provider":"okta"}`
  is rejected with `400`. Okta remains available on the pre-existing
  **OIDC** config path (`ssoProviders`), which is unaffected.
- **One SSO config per `(tenant, provider)`, regardless of protocol.** The DB
  constraint is `UNIQUE (tenant_id, provider)`, not
  `(tenant_id, provider, protocol)` — a tenant cannot have both an OIDC and a
  SAML config for, say, `cognito` at the same time; creating the second
  returns `409`.
- **`name_id_format` isn't settable through the API.** The column and a
  sensible default (`urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress`)
  exist end-to-end in the store layer, but `ssoConfigRequest` (the
  `CreateConfig`/`UpdateConfig` JSON body, `controllers/sso.go`) has no
  `name_id_format` field — every SAML config gets the default with no way to
  override it via `/api/tenant/sso-configs` today.
- **`SAML_REQUEST_TIMEOUT` is dead configuration** — see §3.
- **AuthnRequests are never signed** (`SignAuthnRequests: false`,
  `saml/saml.go`). Fine for Cognito/Entra, which don't require it, but there
  is no SP signing-keypair subsystem if a future IdP does require it.
- **Replay defense is limited to single-use request state + the assertion's
  own expiry.** No separate "used assertion id" tracking beyond the
  single-use `saml_requests` row (bounded by `SAML_REQUEST_STATE_TTL`) and
  whatever `NotOnOrAfter`/`NotBefore` the IdP put in the assertion.

---

## 9. Manual end-to-end testing guide

This could not be exercised against live AWS Cognito / Entra ID in the
environment this was built in (no IdP credentials available). Follow this
with real IdP access.

**Prerequisites:** a deployment with a real, HTTPS-reachable `API_BASE_URL`
(most IdPs require an `https://` ACS/Reply URL for non-sandbox apps — plan on
testing against a real staging deploy, not `localhost`), `SECRET_ENCRYPTION_KEY`
and `FRONTEND_URL` set, and an existing StoneSuite tenant + admin JWT with
`sso_config:configure`.

1. **Create the IdP-side app** per §4 or §5. Note its metadata URL.
2. **Register the config:**
   ```bash
   curl -i -X POST "$API_BASE_URL/api/tenant/sso-configs" \
     -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" \
     -d '{"provider":"cognito","protocol":"saml","metadata_url":"<idp-metadata-url>","enabled":true}'
   ```
   Expect `201`. Confirm `idp_entity_id`, `sso_url`, and
   `certificate_fingerprint` in the response are non-empty — this alone
   proves the backend could fetch and parse the IdP's metadata.
3. **Verify the control-plane row** (control-plane DB):
   ```sql
   SELECT id, tenant_id, provider, protocol, enabled, idp_entity_id, sso_url,
          slo_url, certificate_fingerprint, name_id_format, metadata_fetched_at
   FROM tenant_sso_configs
   WHERE tenant_id = '<tenant-uuid>' AND provider = 'cognito';
   ```
   Don't print `certificate_pem_enc`/`client_secret_enc` to a shared terminal
   or log — they're ciphertext, but treat them as sensitive regardless.
4. **Attempt login** — in a browser, go to:
   ```
   {API_BASE_URL}/api/auth/saml/cognito/initiate?tenant_slug=<slug>
   ```
   Confirm you land on the real IdP login page. Then check:
   ```sql
   SELECT id, tenant_id, provider, expires_at FROM saml_requests
   WHERE tenant_id = '<tenant-uuid>' ORDER BY created_at DESC LIMIT 1;
   ```
   A fresh row confirms `Initiate` recorded request state correctly.
5. **Sign in** at the IdP with a real test user.
6. Confirm the browser lands on
   `{FRONTEND_URL}/auth/sso/callback?code=...` — today this itself 404s/blanks
   in the frontend (§8); what matters here is that the `code` query param is
   present and that the network tab shows the `POST .../acs` returned a
   `302` (not the static "Sign-in failed" HTML).
7. **Verify JWT claims** by driving Exchange directly:
   ```bash
   curl -s -X POST "$API_BASE_URL/api/auth/saml/exchange" \
     -H "Content-Type: application/json" -d "{\"code\":\"<code-from-url>\"}" | jq
   ```
   Confirm `200`, a `token`, and `user.email` matches the IdP test user.
   Decode the JWT payload (e.g. `jwt.io`, or split on `.` and
   `base64 -d` the middle segment) and confirm `id`, `email`, `tenant_id`
   (matches the tenant you initiated against), `exp`, `iat` are present.
8. **Verify the control-plane identity:**
   ```sql
   SELECT id, tenant_id, email, full_name, email_verified,
          sso_provider, sso_subject, sso_session_index
   FROM identities WHERE email = '<test-user-email>';
   ```
   Confirm `tenant_id` matches, `email_verified = true`,
   `sso_provider = 'cognito'` (or `'entra'`), `sso_subject` non-empty.
9. **Verify the tenant-DB user row** (connect to that *tenant's* database,
   not control-plane):
   ```sql
   SELECT id, identity_id, email, full_name, status
   FROM users WHERE identity_id = '<identity-id-from-step-8>';
   ```
   Exactly one row, `status = 'active'`. Then confirm zero roles:
   ```sql
   SELECT * FROM user_roles WHERE user_id = '<user-id-above>';
   ```
   Expect no rows — confirms the fail-closed default from §7. Assign one
   (`POST /api/tenant/users/{userId}/roles`, §7) and confirm the SAML user
   can now call an endpoint that needs it.
10. **Test invalid-signature rejection.** Because request state is single-use,
    you need a fresh `/initiate` round trip you don't let complete normally:
    intercept the browser's `POST .../acs` (e.g. your browser's dev tools
    "copy as cURL", or a proxy) *before* letting it submit, flip one
    character inside the `SAMLResponse` value, and send that as your only
    submission for that request id. Expect `400`, the static error page, and
    a `security_event=saml_signature_invalid` log line (server-side) whose
    `error` field mentions a signature/validation failure — never a panic or
    stack trace in the response.
11. **Test expired-assertion rejection.** Same single-use caveat as above:
    intercept the POST to `/acs`, hold it past the assertion's own
    `NotOnOrAfter` (IdPs typically set this a few minutes out), then forward
    it unmodified. Expect `400`, the static error page, and a
    `saml_signature_invalid` log line whose `error` field reads
    `saml: assertion is outside its valid time window` — this event name
    covers more than literal signature failures (§6, §10).
12. **Test logout.** With a valid exchanged session,
    `POST /api/auth/saml/{provider}/logout` with the JWT. Expect `200`; if
    `slo_available: true`, note the `logout_url`. Confirm StoneSuite's own
    cookies are cleared (`Set-Cookie` with `Max-Age=-1` in the response) even
    before checking SLO. Optionally navigate to `logout_url` to confirm the
    IdP accepts the `LogoutRequest` and redirects back to
    `.../logout-response`, which lands on `{FRONTEND_URL}/auth/login?logged_out=true`
    (the frontend route itself may not exist yet, §8, but the backend redirect
    fires correctly).

---

## 10. Troubleshooting

| Symptom | Likely cause |
|---|---|
| `502` on `POST/PUT /api/tenant/sso-configs...` | `saml.FetchIdPMetadata` failed — `metadata_url` unreachable, not `https://`, timed out (10s), over 2MB, or didn't parse as SAML metadata (missing `IDPSSODescriptor`/`SingleSignOnService`/signing certificate). Load the URL in a plain browser or `curl` first. |
| `503` on `POST/PUT /api/tenant/sso-configs...` or `.../refresh-metadata` | `SECRET_ENCRYPTION_KEY` isn't set on the backend — the cipher is `nil` and `encryptSecret` fails closed rather than ever storing a certificate in plaintext. |
| `400 "metadata_url must be an https:// URL."` | Sent `http://` or a scheme-less value. |
| `409` creating a SAML config | A config for this `(tenant, provider)` already exists — regardless of protocol (§8). `PUT` the existing one, or `DELETE` it first. |
| Signature validation failing at ACS (`saml_signature_invalid` in logs, static "Sign-in failed" page in the browser) | Most often the IdP rotated its signing certificate. Call `POST /api/tenant/sso-configs/{id}/refresh-metadata` to re-pull the current one — nothing does this automatically (§8). Also check the log line's `error` field: this event name is shared with expired-assertion and audience-mismatch failures, not just an actually-bad signature. |
| `403 "This workspace is not available for sign-in."` at `/initiate` | `tenant.Servable()` is false — tenant isn't `active`, migration isn't `ok`, or it has no `db_name` yet. Not SAML-specific; the same gate every tenant route uses. |
| `404 "SAML sign-in is not configured or enabled for this workspace."` at `/initiate` (or the equivalent silent `saml_acs_config_not_found` at ACS) | No `tenant_sso_configs` row with `protocol='saml', provider=<x>, enabled=true` for that tenant. Check `enabled` specifically — a disabled config reads as not-found by design (`GetSSOConfigForAuth`'s `WHERE ... enabled = TRUE`). |
| Browser shows the static "Sign-in failed" page with no detail | By design — ACS never reflects assertion-derived detail to the browser (§6). Search the server security log for the matching `security_event` line around that timestamp/tenant/provider. |
| Post-login redirect lands on a 404 in the frontend | Expected today — `/auth/sso/callback` doesn't exist in the WebUI yet (§8). Confirm the `code` query param is present and call `POST /api/auth/saml/exchange` directly to validate the backend half independently. |
| JIT-provisioned user gets a JWT but every tenant API call `403`s | Expected — new SAML users get zero roles by design (§7). Have a tenant admin assign one. |

---

**Source of truth for everything above:** `saml/*.go`,
`controllers/saml_*.go`, `controllers/sso.go`, `tenancy/sso_config.go`,
`tenancy/sso_domain.go`, `tenancy/identity.go`, `tenancy/saml_request.go`,
`tenancy/saml_login_code.go`, `database/migrations/control_plane/schema.sql`,
`config/config.go`, `main.go`. If behavior here and behavior in the code ever
disagree, the code is right — file a doc fix.

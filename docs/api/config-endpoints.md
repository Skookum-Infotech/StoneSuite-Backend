# Configuration API — Frontend Contract

Endpoints added on `work-planner-tuesday-backend`: **SSO provider configs** and
the **audit-log browser**. This is the contract the frontend consumes — no Go
reading required.

> **RBAC scope model is now two-level: `all` and `own`.** The `team` scope has
> been retired — there is no team management UI or API. Anywhere a scope is
> displayed or selected (e.g. the role editor), offer only `all` and `own`.

## Conventions (all endpoints)
- Base: all routes are under `/api/tenant/` and require the standard auth chain
  (JWT `Authorization: Bearer <token>` or the `auth_token` cookie). The tenant is
  resolved from the token; never sent in the path or body.
- Every response is JSON with `success: boolean`. Errors add `message: string`.
- Shared status codes: `401` unauthenticated, `403` missing permission,
  `404` not found (also returned for another tenant's id — ids are not
  enumerable), `409` conflict, `400` invalid input, `500` server error.
- Timestamps are RFC3339 (`2026-07-19T12:30:45Z`).

---

## SSO Provider Configs
Per-tenant single-sign-on provider settings. Stored in the control plane; the
**client secret (OIDC) and IdP certificate (SAML) are write-only and never
returned** in any response — only a `certificate_fingerprint` is, for SAML.

Permission: `sso_config:read` for GET, `sso_config:configure` for POST/PUT/DELETE
(including `refresh-metadata`).

A config is one of two protocols, `oidc` (default, unchanged from before) or
`saml`. `protocol` decides which of the fields below apply — see
[`docs/SAML_SETUP.md`](../SAML_SETUP.md) for the full SAML setup guide.

### Object
```json
{
  "id": "uuid",
  "tenant_id": "uuid",
  "provider": "entra | cognito | okta",
  "protocol": "oidc | saml",
  "client_id": "string",                  // oidc only, omitted for saml
  "issuer": "https://... (optional, may be \"\")",       // oidc only
  "redirect_uri": "https://... (optional, may be \"\")", // oidc only
  "metadata_url": "https://... (saml only, omitted for oidc)",
  "idp_entity_id": "string (saml only; extracted from IdP metadata)",
  "sso_url": "https://... (saml only; extracted from IdP metadata)",
  "slo_url": "https://... (saml only; may be omitted, not every IdP advertises SLO)",
  "certificate_fingerprint": "hex sha-256 (saml only; the certificate PEM itself is never returned)",
  "name_id_format": "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress (saml only; not settable via this API, always the default)",
  "metadata_fetched_at": "rfc3339 (saml only; set on every create/update/refresh-metadata)",
  "enabled": false,
  "created_at": "rfc3339",
  "updated_at": "rfc3339"
}
```
All `saml`-only fields are `omitempty` and absent on `oidc` configs, and vice
versa for `client_id`/`issuer`/`redirect_uri`.

### `GET /api/tenant/sso-configs`
List all configs for the tenant (newest first).
→ `200 { "success": true, "sso_configs": [ <config>, ... ] }`

### `GET /api/tenant/sso-configs/{id}`
→ `200 { "success": true, "sso_config": <config> }` · `404` if not found.

### `POST /api/tenant/sso-configs`
Body (`protocol` decides which other fields are validated/required):
```json
{
  "provider": "entra",           // required. protocol=oidc: one of entra|cognito|okta. protocol=saml: one of entra|cognito only (case-insensitive)
  "protocol": "oidc",            // optional, "oidc" (default) or "saml"
  "client_id": "string",         // oidc: required. ignored for saml
  "client_secret": "string",     // oidc: required on create, stored encrypted, never echoed. ignored for saml (saml has no client secret)
  "issuer": "https://...",       // oidc only; if present must be an http(s) URL
  "redirect_uri": "https://...", // oidc only; if present must be an http(s) URL
  "metadata_url": "https://...", // saml: required, must be https://. ignored for oidc
  "enabled": false               // optional, default false
}
```
For `protocol: "saml"`, the IdP metadata document at `metadata_url` is fetched
synchronously on every create — `idp_entity_id`, `sso_url`, `slo_url`, the
signing certificate (encrypted at rest), and `certificate_fingerprint` are all
derived from it, not supplied by the caller.
→ `201 { "success": true, "sso_config": <config> }`
- `400` invalid provider/protocol / missing client_id / missing client_secret / malformed URL / missing or non-https metadata_url.
- `409` a config for this provider already exists (unique per tenant+provider, **regardless of protocol** — a tenant can't have both an oidc and a saml config for the same provider).
- `502` (saml only) the metadata URL was unreachable or didn't parse as SAML metadata.
- `503` secret encryption is not configured on the server (cannot store the client secret or, for saml, the IdP certificate).

### `PUT /api/tenant/sso-configs/{id}`
Same body as create, except **`client_secret` is optional** — omit or send `""`
to keep the stored secret; send a new value to replace it. For
`protocol: "saml"`, `metadata_url` is always re-fetched (there is no
"unchanged, skip the fetch" case).
→ `200 { "success": true, "sso_config": <config> }` · `400` / `404` / `409` / `502` / `503` as above.

### `POST /api/tenant/sso-configs/{id}/refresh-metadata`
SAML only — re-fetches the stored `metadata_url` and updates the IdP entity
id, SSO/SLO URLs, certificate, and fingerprint in place. No body. Safe to call
repeatedly (e.g. from an external cron, since nothing in the backend refreshes
this automatically).
→ `200 { "success": true, "sso_config": <config> }`
- `400` the target config is not `protocol: "saml"`.
- `404` not found. `502` metadata URL unreachable/malformed. `503` secret encryption unavailable.

### `DELETE /api/tenant/sso-configs/{id}`
→ `200 { "success": true }` · `404` if not found.

> **SAML: the login flow now exists.** SP-initiated SAML 2.0 login (AWS
> Cognito, Microsoft Entra ID) is implemented — see
> [`docs/SAML_SETUP.md`](../SAML_SETUP.md) for the endpoints, request/response
> shapes, and setup walkthroughs. It is a separate route tree
> (`/api/auth/saml/...`), not part of this file.
>
> **OIDC: still config-only.** For `protocol: "oidc"` configs, this file's
> original caveat still holds — no OIDC login flow (authorize redirect,
> callback, token exchange) exists. These endpoints only manage configuration
> for that protocol. Do not build a "Sign in with SSO" button against an
> `oidc` config yet.

---

## Audit-Log Browser
Tenant-wide audit trail. Permission: `audit:read`. Results are **narrowed by the
caller's scope** on the acting user: `all` = every entry; anything else = the
caller's own entries only. Keyset (cursor) pagination — no offsets.

### `GET /api/tenant/audit`
Query params (all optional):
| param | meaning |
|-------|---------|
| `resource` | exact resource match (e.g. `quote`, `customer`) |
| `action` | exact action match (e.g. `create`, `transition`) |
| `actor` | actor user id (uuid) exact match |
| `from` | RFC3339; `created_at >=` |
| `to` | RFC3339; `created_at <=` |
| `limit` | 1–100, default 25 |
| `cursor` | opaque `next_cursor` from the previous page |

→ `200`
```json
{
  "success": true,
  "entries": [
    {
      "id": "uuid",
      "actor_user_id": "uuid | null",
      "action": "string",
      "resource": "string",
      "resource_id": "string",
      "details": { },
      "created_at": "rfc3339"
    }
  ],
  "next_cursor": "opaque-string-or-empty"
}
```
- Paginate by passing `next_cursor` back as `cursor`. An empty `next_cursor`
  means the last page.
- `400` malformed `from`/`to`, negative `limit`, or an invalid `cursor`.

### Frontend notes
- Ordering is `created_at DESC` — newest first; keep entries in returned order.
- `actor_user_id` is `null` for actions taken via the v2 employee path (the
  employee id lives inside `details`).
- Do not construct or mutate `cursor` client-side; treat it as opaque.

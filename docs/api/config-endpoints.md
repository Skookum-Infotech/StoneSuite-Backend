# Configuration API — Frontend Contract

Endpoints added on `work-planner-tuesday-backend`: **SSO provider configs**,
**teams & membership**, and the **audit-log browser**. This is the contract the
frontend consumes — no Go reading required.

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
**client secret is write-only and is never returned** in any response.

Permission: `sso_config:read` for GET, `sso_config:configure` for POST/PUT/DELETE.

### Object
```json
{
  "id": "uuid",
  "tenant_id": "uuid",
  "provider": "entra | cognito | okta",
  "client_id": "string",
  "issuer": "https://... (optional, may be \"\")",
  "redirect_uri": "https://... (optional, may be \"\")",
  "enabled": false,
  "created_at": "rfc3339",
  "updated_at": "rfc3339"
}
```

### `GET /api/tenant/sso-configs`
List all configs for the tenant (newest first).
→ `200 { "success": true, "sso_configs": [ <config>, ... ] }`

### `GET /api/tenant/sso-configs/{id}`
→ `200 { "success": true, "sso_config": <config> }` · `404` if not found.

### `POST /api/tenant/sso-configs`
Body:
```json
{
  "provider": "entra",          // required, one of entra|cognito|okta (case-insensitive)
  "client_id": "string",        // required
  "client_secret": "string",    // required on create; stored encrypted, never echoed
  "issuer": "https://...",       // optional; if present must be an http(s) URL
  "redirect_uri": "https://...", // optional; if present must be an http(s) URL
  "enabled": false               // optional, default false
}
```
→ `201 { "success": true, "sso_config": <config> }`
- `400` invalid provider / missing client_id / missing client_secret / malformed URL.
- `409` a config for this provider already exists (unique per tenant+provider).
- `503` secret encryption is not configured on the server (cannot store secret).

### `PUT /api/tenant/sso-configs/{id}`
Same body as create, except **`client_secret` is optional** — omit or send `""`
to keep the stored secret; send a new value to replace it.
→ `200 { "success": true, "sso_config": <config> }` · `404` / `409` / `503` as above.

### `DELETE /api/tenant/sso-configs/{id}`
→ `200 { "success": true }` · `404` if not found.

> **Not yet implemented (separate follow-up):** the OAuth login flow itself —
> authorize redirect, callback, token exchange, identity linking. These endpoints
> only manage configuration. Do not build a "Sign in with SSO" button against them yet.

---

## Teams & Membership
Workspace teams. Stored in the tenant DB. Permission: `team:read` for GET,
`team:configure` for mutations.

### Objects
```json
// list item
{ "id": "uuid", "name": "string", "member_count": 3, "created_at": "rfc3339" }

// detail (GET by id) adds members
{
  "id": "uuid", "name": "string", "member_count": 2, "created_at": "rfc3339",
  "members": [ { "user_id": "uuid", "email": "a@x.com", "full_name": "A B" } ]
}
```

### `GET /api/tenant/teams`
→ `200 { "success": true, "teams": [ <list item>, ... ] }`

### `GET /api/tenant/teams/{id}`
→ `200 { "success": true, "team": <detail> }` · `404` if not found.

### `POST /api/tenant/teams`
Body: `{ "name": "string" }` (required, non-blank).
→ `201 { "success": true, "team": <list item> }` · `400` blank name.

### `PUT /api/tenant/teams/{id}`
Body: `{ "name": "string" }`. → `200 { "success": true, "team": <list item> }` ·
`400` blank name · `404` not found.

### `DELETE /api/tenant/teams/{id}`
Deletes the team; membership rows cascade. → `200 { "success": true }` · `404`.

### `POST /api/tenant/teams/{id}/members`
Body: `{ "user_id": "uuid" }`. Idempotent (re-adding a member is a no-op).
→ `200 { "success": true }`
- `404` team not found · `400` user not found · `400` missing user_id.

### `DELETE /api/tenant/teams/{id}/members/{userId}`
Removing a non-member is a no-op success. → `200 { "success": true }` · `404` team not found.

---

## Audit-Log Browser
Tenant-wide audit trail. Permission: `audit:read`. Results are **narrowed by the
caller's scope** on the acting user: `all` = every entry; `team` = entries by
users sharing a team with the caller (plus the caller); `own` = the caller's own
entries only. Keyset (cursor) pagination — no offsets.

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

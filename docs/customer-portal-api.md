# Customer Portal — API Contract

The customer portal is a second, external-facing surface: an approved customer signs in
and sees their own sales orders, invoices, payments and refunds. It is served by this
backend under `/api/portal/`; the UI lives in the frontend repo.

This document is the contract the frontend builds against.

## What a portal session is, and is not

A portal user is **not** a member of the workspace. They have no `users` row and no
`employee` row, hold no roles and no permissions, and cannot reach any `/api/tenant/*` or
`/api/platform/*` route — the token is refused at the middleware, before any handler runs.
Their access is exactly: the documents belonging to the one customer record they are
linked to, in states that have been issued to them.

A customer may be linked to **more than one tenant** (they buy from two StoneSuite
workspaces). One login, several workspaces, **one active at a time** — switching works
like the internal role switcher: the server re-mints the token, the client reloads.

## Authentication

Portal tokens carry `kind: "portal"` and are otherwise the same HS256 JWT as staff tokens
— same `auth_token` httpOnly cookie, same `Authorization: Bearer` fallback, same CSRF
double-submit when `COOKIE_SAME_SITE_MODE=none`.

### `POST /api/portal/auth/login`

```json
{ "email": "buyer@acme.test", "password": "..." }
```

```json
{
  "success": true,
  "token": "eyJ...",
  "expiresAt": 1755600000000,
  "tenantId": "8f3c...",
  "workspaces": [
    { "tenantId": "8f3c...", "name": "Elevation Stone", "slug": "elevation", "active": true },
    { "tenantId": "b21a...", "name": "Northgate Surfaces", "slug": "northgate", "active": false }
  ],
  "user": { "id": "...", "email": "buyer@acme.test", "fullName": "Buyer One" }
}
```

The session starts on the first workspace. Render the switcher whenever `workspaces` has
more than one entry.

**Every failure returns the same 401**, `"Invalid email or password."` — unknown address,
wrong password, revoked access, and "this is a staff account" are deliberately
indistinguishable. Do not try to branch on the message.

### `POST /api/portal/auth/switch-workspace`

```json
{ "tenantId": "b21a..." }
```

Returns a **new token** (`token`, `expiresAt`, `tenantId`, `workspaceName`) and resets the
cookie. Discard any cached document data and refetch — the previous workspace's records
are not valid in the new one. A workspace the customer is not linked to returns **403**.

### `GET /api/portal/workspaces`

Returns `{ success, workspaces: [...] }` with `active` marking the current one. Use this
to repopulate the switcher after a page reload.

### Other auth routes

| Route | Notes |
|---|---|
| `POST /api/portal/auth/refresh` | Send `{ "tenantId": "<current workspace>" }` so the session resumes where it was. The access token is expired by then, so the server cannot infer it — and omitting it resumes the *first* linked workspace, which is wrong for a multi-workspace customer. A workspace you are not linked to is refused. Do not use `/api/auth/refresh`: that is the staff path. |
| `POST /api/portal/auth/logout` | Revokes the refresh token and clears cookies. |
| `GET /api/portal/auth/invite/{token}` | Validates an invitation without consuming it. See below. |
| `POST /api/portal/auth/accept-invite` | `{ token, password }`. Minimum 8 characters. Single-use. |
| `POST /api/portal/auth/forgot-password` | `{ email }`. **Always 200**, whatever the address. |
| `GET /api/portal/auth/reset-password/{token}` | Validates a reset link; returns `{ email, fullName }`. |
| `POST /api/portal/auth/reset-password` | `{ token, password }`. |
| `POST /api/portal/auth/change-password` | `{ currentPassword, newPassword }`. Authenticated. |

Invitation and password-reset are **separate token systems**. An invitation token
(`portal_invites`) is only valid at `accept-invite`; a reset token is only valid at
`reset-password`. Do not send one to the other.

### `GET /api/portal/auth/invite/{token}`

On success (200):

```json
{ "success": true, "status": "pending", "email": "buyer@acme.test",
  "fullName": "Buyer One", "workspaceName": "Elevation Stone",
  "expiresAt": "2026-08-20T14:00:00Z" }
```

On failure it returns 400 **with a `status` field** — branch on it, because the three
cases need different screens:

| `status` | What to show |
|---|---|
| `expired` | "This invitation has expired — ask your contact for a new one." |
| `accepted` | "Already used" plus a link to sign in. |
| `revoked` | "No longer valid." |

Invitations last `INVITE_EXPIRY_HOURS` (default 24), the same setting as every other
invite in the product. There is no self-service resend — staff resend from the customer
record, which is why the expired screen should tell the customer who to contact rather
than offering a button.

Unauthenticated auth routes are rate limited per IP (0.2 req/s sustained, burst 10).
Expect **429** under rapid retries.

## Profile

### `GET /api/portal/me`

```json
{
  "success": true,
  "profile": { "email": "buyer@acme.test", "fullName": "Buyer One", "phone": "555-0100" },
  "customer": { "id": "c7f1-...", "name": "Acme Stone" }
}
```

### `PATCH /api/portal/me`

Accepts `fullName` and/or `phone`. Omitted fields keep their current value.

**Email is not editable.** It is the login and is globally unique across all tenants;
changing it is an identity operation, not a profile edit.

## Documents

Four modules, identical shape:

```
GET  /api/portal/sales-orders          GET  /api/portal/sales-orders/{uuid}
POST /api/portal/sales-orders/search
GET  /api/portal/invoices              GET  /api/portal/invoices/{uuid}
POST /api/portal/invoices/search
GET  /api/portal/payments              GET  /api/portal/payments/{uuid}
POST /api/portal/payments/search
GET  /api/portal/refunds               GET  /api/portal/refunds/{uuid}
POST /api/portal/refunds/search
```

**List** (`GET`) takes `?cursor=` and `?limit=` (default 25, max 100) and returns:

```json
{ "success": true, "records": [ ... ], "nextCursor": "eyJ...", "hasMore": true }
```

Pagination is keyset, not offset: pass `nextCursor` back to get the next page. Do not
construct cursors.

**Search** (`POST`) takes the same filter/sort body as the internal search endpoints.
An unknown filter field returns **400** with a message naming the field. Field keys are
whitelisted per module.

A `customer_id` filter is accepted but pointless: it is ANDed onto the session's own
customer, so it can only narrow to nothing.

**Detail** (`GET .../{uuid}`) returns `{ success: true, record: {...} }` with the full
document body — line items, all totals, and the frozen billing and shipping snapshots —
so the frontend can render the printable PDF client-side. There is no server-side PDF
endpoint by design.

Detail responses omit internal attribution: no owner or sales-rep names, no
`createdBy`/`approvedBy`, no approval state, no internal notes, no history timeline.

### What the customer can see

Finalized documents only. Drafts and anything awaiting internal approval are invisible —
a customer must never see a figure staff are still editing.

| Module | Visible states |
|---|---|
| Sales orders | `APPV`, `OPEN`, `PART`, `FILL`, `CANC` |
| Invoices | `SENT`, `PART`, `PAID`, `ODUE`, `VOID` |
| Payments | `APPV`, `DEPO`, `VOID` |
| Refunds | `APPV`, `SENT`, `VOID` |

An invoice that is approved but not yet `SENT` has not been issued, so it is hidden. A
`VOID` invoice stays visible — a customer who received it should see it was voided rather
than have it silently disappear.

A document that is out of scope, in a hidden state, or belongs to another customer returns
**404**, never 403. There is no way to distinguish those cases, by design.

## Messages

```
GET  /api/portal/{documents}/{uuid}/messages
POST /api/portal/{documents}/{uuid}/messages   { "body": "When is this due?" }
```

`{documents}` is the same path segment the document itself is served under —
`sales-orders`, `invoices`, `payments`, `refunds`. The thread hangs off the document's own
URL, so `/api/portal/invoices/{uuid}` and `/api/portal/invoices/{uuid}/messages` are the
same resource. Body is capped at 4000 characters. A message on a document the customer
cannot see returns **404**.

```json
{ "success": true, "messages": [
  { "id": "...", "module": "invoice", "documentId": "...", "authorKind": "portal",
    "authorName": "Buyer One", "body": "When is this due?", "createdAt": "..." }
] }
```

`authorKind` is `portal` or `staff` — use it to side the message rather than inferring
from the name.

### Staff side of the same thread

```
GET  /api/tenant/{documents}/{uuid}/portal-messages
POST /api/tenant/{documents}/{uuid}/portal-messages  { "body": "It is due on the 30th." }
```

Again `{documents}` is `sales-orders` / `invoices` / `payments` / `refunds`.

Permission follows the **document**, not a separate one: reading a thread needs
`<module>:read`, replying needs `<module>:update`, and ownership scope applies exactly as
it does to the document itself. Staff see the thread regardless of the document's status,
including on drafts the customer cannot see.

Replying requires the staff member to have an employee profile — a reply must be
attributable. Without one the endpoint returns **403** with that reason.

## Staff side — granting access

These are internal routes, on the normal tenant chain, gated on the new `portal_access`
permission (`create` / `read` / `delete`). Deliberately separate from `customer:update`:
creating a portal login mints an external credential, which is a security act rather than
a CRM edit, so a tenant can let sales staff edit customers without also letting them
create outside logins.

| Route | Permission |
|---|---|
| `GET /api/tenant/customers/{customerUuid}/portal-users` | `portal_access:read` |
| `POST /api/tenant/customers/{customerUuid}/portal-users` | `portal_access:create` |
| `DELETE /api/tenant/customers/{customerUuid}/portal-users/{id}` | `portal_access:delete` |
| `POST /api/tenant/customers/{customerUuid}/portal-users/{id}/resend` | `portal_access:create` |

`POST` takes `{ email, fullName }` and returns **201**. It returns:

- **409** if the customer is not an approved `CUST`-stage record. Approval is required to
  *grant* access — surface this as "approve this customer first", not as a generic error.
- **409** if the email already belongs to a workspace user. One address must not be both
  an employee login and a customer login.

Several people at one customer can each have their own login; revoking one does not affect
the others.

The invite link is sent **by email only** and is never returned in the response body, so
holding `portal_access:create` does not by itself yield a working login for someone else's
address. There is no "copy invite link" affordance to build.

Each portal login in the list response carries an `inviteStatus`, which is what drives the
UI:

| `inviteStatus` | Meaning | Show |
|---|---|---|
| `pending` | Invited, not yet accepted, still in date | "Invited — expires {inviteExpiresAt}" |
| `expired` | Invited, never accepted, past expiry | "Invitation expired" + **Resend** |
| `accepted` | They have set a password | Nothing, or "Active since…" |
| `none` | No invite on record (already had a login elsewhere) | Nothing |

**Resend** mints a new token and re-sends the email; the previous link stops working
immediately, so resending is also how you kill a link that was forwarded to the wrong
person. It returns **409** if the customer has already set a password — at that point
they should use "forgot password" instead.

Revoking access also cancels any invitation still in flight.

Once granted, access is governed by its own status, not by the customer's approval flag —
that flag resets on every CRM transition, so a routine renewal would otherwise lock the
customer out mid-session. Revoking is explicit, and it kills live sessions immediately:
the tenant-side row, the control-plane link, and every refresh token are all revoked.

## Error shape

Every error is `{ "success": false, "message": "..." }`.

| Status | Meaning in the portal |
|---|---|
| 400 | Malformed body, or an unknown filter/sort field |
| 401 | Not signed in, session expired, or portal access no longer available |
| 403 | Wrong token class for this surface, or a workspace you are not linked to |
| 404 | Document not found **or** not yours **or** not in a visible state |
| 409 | Customer not approved, or email already a workspace user |
| 429 | Rate limited |

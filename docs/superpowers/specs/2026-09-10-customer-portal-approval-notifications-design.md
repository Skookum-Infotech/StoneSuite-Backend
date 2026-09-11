# Customer Portal Approval Notifications — Design Spec

**Date:** 2026-09-10 · **Status:** Draft, pending user review
**Repos affected:** `StoneSuite-Backend` (new code), `StoneSuite-WebUI` (small unhide change).
`stonesuite-notify` needs **no changes** — see §2.

## 1. Problem

A customer sent a Sales Order, Invoice, Payment, or Refund never hears anything back once
staff finish approving it internally. Today the only thing a customer ever receives is the
one-time "here's your document" email from the document-send flow
(`controllers/documents.go`). There is no "this was approved and is ready" signal, in-app or
email — the customer portal's `NotificationBell` is explicitly hidden
(`src/components/NotificationBell.tsx:29-34`, "there is nothing this bell could ever show a
customer"), and the internal `approvalchain` notification machinery structurally can't reach
a customer even if the bell were shown (its recipient resolution only walks
`employee → users → identity_id`, see `contact`'s doc comment in `approvalchain/notify.go`).

## 2. Current state (verified by reading the code directly)

- **The customer portal is not a separate identity system from staff.** A portal JWT's `"id"`
  claim is a **control-plane `identities.id`** — the same id space a staff session's JWT uses.
  Traced through `controllers/portal_auth.go`'s `Login` (`h.CP.IdentityByEmail`,
  `h.CP.PortalTenantsForIdentity`) and `generatePortalJWT` (`controllers/tenant.go:223`, sets
  `"id": identityID`). `identity_tenants` (`database/migrations/control_plane/schema.sql:349`)
  is what lets one control-plane identity reach multiple tenants as a portal principal — this
  is the intentional multi-tenant-customer design (its own doc comment: "a customer... may buy
  from two StoneSuite tenants and needs one login that reaches both").
  - There is a second, apparently unrelated `customer_identities` (tenant-local) +
    `RequireCustomerAuth`/`principal_type=customer` system (`middleware/customer_auth.go`,
    routes under `/api/customer/*`). This is **not** the system behind the document-viewing
    portal or `NotificationBell` — that one is `kind=portal` / `/api/portal/*`. Out of scope
    here; not touched.
- **Consequence: `stonesuite-notify` needs zero changes.** It already scopes the bell purely
  by `(tenant_id, recipient_user_id)`, and a portal JWT already presents a `recipient_user_id`
  in exactly the same id space it already expects. `middleware/auth.go` in `stonesuite-notify`
  doesn't even inspect a `kind`/`principal_type` claim today — any validly-signed token is
  treated the same way.
- **The link from a document to its customer's portal identity already exists**, just not
  wired up: control-plane `portal_invites` (`database/migrations/control_plane/schema.sql:381`)
  stores both `identity_id` and the tenant's `customer_uuid` **on the same row**, and the row
  persists after acceptance (no deletion on accept, just `status='accepted'`,
  `accepted_at` set). `tenancy.ControlPlane` (`tenancy/portal_invites.go`) has no method to
  look this up by `customer_uuid` yet — needs one new method.
- **`approvalchain` and every module package (`invoice`, `payment`, etc.) only ever touch the
  tenant pool** — none of them have a `*tenancy.ControlPlane` reference, and CLAUDE.md forbids
  reaching one via a package-level global. The **controllers layer** is where a
  `*tenancy.ControlPlane` is normally injected — but today's relevant controllers
  (`InvoiceOps`, `SalesOrderOps`, `PaymentOps`, `RefundOps`) are constructed with **no
  fields at all** (e.g. `InvoiceOps struct{}`, `NewInvoiceOps()`), resolving the tenant pool
  per-request via `tenancy.PoolFromContext`. This is the one real structural change this
  feature needs: give those 4 controllers a `cp *tenancy.ControlPlane` field.
- **`services.NotificationRequest.Link` is not populated by any existing caller today** —
  confirmed by grep: neither `approvalchain/notify.go`'s `sendApprovalNotification` nor
  `controllers/documents.go`'s customer-send/owner-notify calls ever set `Link`. The frontend
  (`AppNotification.link`, and the click-to-navigate handler in `NotificationBell.tsx`) is
  already wired to use it, but nothing has exercised that path yet. This feature will be the
  first caller to set it.
- **Portal and staff sessions use the exact same detail routes** — confirmed via
  `src/hooks/useUserPermissions.ts`'s `PORTAL_GRANTS` allowlist (`'sales_order:read'`,
  `'invoice:read'`, `'payment:read'`, `'refund:read'`), which makes `PermissionGuard` pass for
  a portal session on the same routes staff use: `/sales/sales_order/:id`,
  `/sales/invoice/:id`, `/sales/payment/:id`, `/sales/refund/:id`
  (`src/router/index.tsx`). List pages already branch on `kind === 'portal'` to hide
  staff-only actions (e.g. `InvoiceListPage.tsx`) but render the same detail page underneath.
  **No portal-specific route or link shape is needed** — one `Link` value serves both
  audiences.

## 3. Scope (per user decision)

- **Trigger:** the moment internal approval finalizes on a document (not every status change —
  just "approval is done, quorum met or super-admin override").
- **Modules:** Sales Order, Invoice, Payment, Refund only — the four the portal already shows
  and that have an approval gate. (Not Quote/Estimate — portal never shows those. Not
  Credit Memo — portal doesn't list it either, per `main.go`'s `/api/portal/*` routes.)
- **Channels:** portal bell (in-app row, unconditional per stonesuite-notify's existing
  behavior) **and** email — matching how internal approval notifications already work.
- **Frontend:** un-hide `NotificationBell` for portal sessions — no new component, no new
  routes.

## 4. Design

### 4.1 `tenancy` package addition

New method on `tenancy.ControlPlane` (`tenancy/portal_invites.go`), alongside the existing
`PortalInviteByToken`/`PendingPortalInviteByEmail`/etc.:

```go
// PortalInviteForCustomer returns the accepted portal invite linking
// customerUUID (a tenant-DB customer.customer_uuid) to a control-plane
// identity, or ErrPortalInviteNotFound if this customer has no active
// portal login -- the caller treats that as "nothing to notify", not an
// error.
func (c *ControlPlane) PortalInviteForCustomer(ctx context.Context, tenantID, customerUUID string) (*PortalInvite, error)
```

Query: `SELECT ... FROM portal_invites WHERE tenant_id = $1 AND customer_uuid = $2 AND status = 'accepted' ORDER BY accepted_at DESC LIMIT 1`. A customer with a *revoked* portal link (see
`RevokePortalLink`) should not be notified — revocation doesn't currently touch
`portal_invites.status`, so the resolver additionally checks `PortalLinkActive` (existing
method) before treating the result as notifiable, so a revoked customer stops getting
notifications the same request cycle their access is pulled.

### 4.2 New controller-layer notify helper

A new small file, e.g. `controllers/customer_notify.go`, with one function:

```go
// notifyCustomerApproved best-effort-notifies a document's customer, via
// their portal login if they have one, that it's been approved. No-ops
// silently (logs and returns) if the customer has no active portal access --
// this is not an error, just nothing to do. Never blocks or fails the
// caller's HTTP response, matching every other Notify* failure contract in
// this codebase.
func notifyCustomerApproved(ctx context.Context, cp *tenancy.ControlPlane, tenantID, customerUUID, resource, displayName, number, recordUUID string)
```

Behavior: resolve the portal invite (§4.1) → resolve current email via
`cp.IdentityByID` (not the invite's snapshot email, which can go stale if the customer
changed their portal email since accepting) → `services.SendNotification` with:

| Field | Value |
|---|---|
| `EventType` | `"<resource>.customer_approved"` (distinct from the internal `"<resource>.approved"` event — different audience, different copy) |
| `Recipients` | one `{UserID: identityID, Email: currentEmail}` |
| `Title` | `"Your <DisplayName> <number> has been approved"` |
| `Body` | `"Approved."` |
| `Link` | `"/sales/<route-segment>/<recordUUID>"` — the same route staff use (§2) |
| `Channels` | `["email"]` (in-app row created unconditionally, same convention as every existing `Notify*` call) |

### 4.3 Controller wiring

`InvoiceOps`, `SalesOrderOps`, `PaymentOps`, `RefundOps` each gain a `cp *tenancy.ControlPlane`
field and constructor parameter (mirroring how other controllers that need control-plane
access are already built) — updated at their one `New*Ops(...)` call site in `main.go`.

Each module's `Approve` handler, after its existing `<module>.Approve(...)` call succeeds:
check whether the returned record's approval status is now the finalized value (the same
condition each module's own `notifyApproved`/`NotifyApproved` internal call already fires on
— for the 3 engine-based modules here, that's `ApprovalStatusColumn == approvalchain.StatusApproved`
on the returned record; for sales_order's own hand-rolled `approval.go`, its equivalent local
constant). If finalized, call `notifyCustomerApproved(...)` with the record's own
`CustomerUUID` and number field (already present on every one of these return types, e.g.
`invoice.Invoice.CustomerUUID`) — **after** the existing internal `notifyApproved` call, as an
additional, independent notification, not a replacement.

*Exact field/constant names to confirm against source at implementation time (this is a
design-level spec, not a line-level diff) — the shape above is verified correct, the precise
identifiers are not all pinned down.*

### 4.4 Frontend

`src/components/NotificationBell.tsx:34` — remove (or invert) the `enabled = isAuthenticated
&& !isCustomer` guard so a portal session polls and renders the bell like a staff session.
No other change: the same `unreadCount`/`list`/`markRead`/`markAllRead` calls already work
against `stonesuite-notify` for any valid JWT (§2), and the click-to-navigate behavior added
in the prior session already uses `n.link` generically.

## 5. Out of scope

- The separate `customer_identities`/`RequireCustomerAuth` system (§2) — untouched, unrelated.
- Any event other than approval finalization (no "submitted", "rejected", "still needs
  approval" customer-facing equivalents — those are internal-staff concepts the customer never
  saw in the first place).
- Quote, Estimate, Credit Memo, or any other module — the portal doesn't show them today.
- Populating `Link` on the existing *internal* staff-facing approval notifications — noted in
  §2 as a related, currently-unexercised gap, but not part of what was asked here. Worth a
  separate fast-follow if wanted.
- A customer-facing notification-preferences screen (backlogged separately, see the sibling
  notification plan).
- SMS/push to customers — email + portal bell only, per §3.

## 6. Testing

- `tenancy`: table-driven / dbtest coverage for `PortalInviteForCustomer` (found, not found,
  revoked-but-still-accepted-row edge case per §4.1).
- `controllers`: dbtest coverage for at least one of the four `Approve` handlers (invoice, as
  the reference engine-based module) asserting `notifyCustomerApproved` fires exactly once on
  finalization and not on a partial (non-finalizing) sign-off, and that it no-ops cleanly when
  the customer has no portal access.
- Frontend: manual/browser verification that the bell renders for a portal session (existing
  automated coverage for the bell's core behavior — badge, click-through — already exists from
  the prior session's work; this only removes a guard).
- After implementation: `tenancy-security-reviewer` (this touches auth-adjacent
  control-plane/portal code) and `module-drift-checker` for each of the 4 touched modules.

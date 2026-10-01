# Resend Delivery Status — Design Spec

**Date:** 2026-10-01 · **Status:** Draft — awaiting review
**Repos touched:** `stonesuite-notify` (primary), `StoneSuite-Backend`, `StoneSuite-WebUI`
**Related:** `services/notify_delivery.go` (`ConfirmEmailDelivery`), memory note *notify-email-delivery-is-async*

---

## 1. Problem

Every email StoneSuite sends travels `WebUI → Backend → stonesuite-notify → Resend`.
Today the only truth the user ever gets is the *first attempt*: `ConfirmEmailDelivery`
polls notify for up to 8 s and reports `sent` once **Resend accepted** the message.
That is not delivery. Resend reports what happens next — `delivered`, `delivery_delayed`,
`bounced`, `complained`, `failed`, `suppressed` — and none of it reaches us:

- `channels.sendViaResend` discards Resend's response body on success, so the **Resend
  email id is never stored** and no later event can be matched to a send.
- Notify has **no inbound webhook route**.
- The backend only polls the notify queue state (`pending → sent/retrying/failed`),
  which describes our hand-off to Resend, not the recipient's mailbox.

**Goal:** the user who triggered an email can see, per send, the real Resend outcome, and is
alerted in-app when it goes wrong.

## 2. Decisions (from brainstorming)

| # | Question | Decision |
|---|---|---|
| 1 | How does the sender find out? | **Both**: a status badge per send *and* an in-app alert on problems. |
| 2 | Which emails? | **Every email type** that has a user-visible send/invite record. |
| 3 | Data flow | **Resend → webhook into notify → backend reads from notify on demand.** No backend webhook, no status columns copied into domain tables. |
| 4 | Alert policy | **Problems only**: `bounced`, `complained`, `failed`, `suppressed`, `delivery_delayed`. `sent`/`delivered` only move the badge. |
| 5 | Frontend | **In scope** (`StoneSuite-WebUI`). |

Rejected: pushing every event notify → backend (needs a status column + update path per table, tenant-DB
resolution per event, and retry/idempotency on the extra hop); webhook straight into the backend
(notify owns the Resend relationship and the email id).

## 3. Architecture

```
Resend ──(Svix-signed webhook)──▶ notify  POST /api/webhooks/resend
                                   │  verify → dedupe → advance state (tx)
                                   │  └─ first problem state ─▶ in-app alert to the sender
                                   ▼
                     notification_deliveries.provider_status  (+ email_events log)
                                   ▲
Backend ──POST /api/deliveries/email-status {tenantId, notificationIds[]}──┘
   │  services.EmailStatuses → worst-of fold → emailStatus on list/detail rows
   ▼
WebUI  <EmailStatusBadge>
```

`ConfirmEmailDelivery` is unchanged: it still answers "did the first attempt work?" at request
time. This feature adds the **after-the-fact lifecycle**.

## 4. stonesuite-notify

### 4.1 Schema (`database/migrations/schema.sql`, idempotent, appended)

```sql
ALTER TABLE notification_deliveries ADD COLUMN IF NOT EXISTS provider_email_id  TEXT;
ALTER TABLE notification_deliveries ADD COLUMN IF NOT EXISTS provider_status    VARCHAR(24);
ALTER TABLE notification_deliveries ADD COLUMN IF NOT EXISTS provider_status_at TIMESTAMPTZ;
ALTER TABLE notification_deliveries ADD COLUMN IF NOT EXISTS provider_detail    TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_deliveries_provider_email
    ON notification_deliveries (provider_email_id) WHERE provider_email_id IS NOT NULL;

ALTER TABLE notifications ADD COLUMN IF NOT EXISTS status_link TEXT;   -- §4.5

CREATE TABLE IF NOT EXISTS email_events (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    svix_id           TEXT        NOT NULL UNIQUE,            -- webhook idempotency key
    delivery_id       UUID        NOT NULL REFERENCES notification_deliveries(id) ON DELETE CASCADE,
    tenant_id         UUID        NOT NULL,
    provider_email_id TEXT        NOT NULL,
    event_type        VARCHAR(32) NOT NULL,
    occurred_at       TIMESTAMPTZ NOT NULL,
    detail            TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_email_events_delivery ON email_events (delivery_id, occurred_at);
```

`notification_deliveries.status` is the **send-queue** state the workers claim on; it is never
touched by webhooks. Provider lifecycle lives only in the new columns. The raw payload is **not**
stored (it carries recipient and subject); only type, time and a short detail string are.

### 4.2 Capture the Resend id

`channels.SendNotificationEmail` returns `(providerID string, err error)`; `sendViaResend` decodes
`{"id": "..."}` from the 2xx body (SMTP returns `""`). The worker passes the id to the delivery
store with `MarkSent`, which writes `provider_email_id` in the same statement. A body that can't be
parsed is not an error — the email was accepted — the row just has no id and will only ever show
`sent`. Same for the SMTP fallback (no webhooks exist for it).

### 4.3 State model

Webhook event → `provider_status` and a rank. State only moves **forward**; an event with a lower
rank than the current state is recorded in `email_events` but does not change the row. Equal rank
with a different status keeps the first (no overwrite, no second alert).

| Resend event | `provider_status` | rank | problem? |
|---|---|---|---|
| `email.sent` | `sent` | 10 | no |
| `email.delivery_delayed` | `delivery_delayed` | 20 | **yes** |
| `email.delivered` | `delivered` | 30 | no |
| `email.complained` | `complained` | 40 | **yes** |
| `email.bounced` | `bounced` | 50 | **yes** |
| `email.failed` | `failed` | 50 | **yes** |
| `email.suppressed` | `suppressed` | 50 | **yes** |

Other event types (`opened`, `clicked`, `scheduled`, `received`, …) are acknowledged with 200 and ignored.

**Derived client state** (returned by §4.6): `provider_status` when set, otherwise from the queue
`status`: `pending`/`processing` → `queued`, `sent` → `sent`, `retrying` → `retrying`, `failed` → `failed`,
`skipped` → `skipped`.

### 4.4 Webhook endpoint

`POST /api/webhooks/resend` — registered on the mux **outside** `RequireAuth` and `RequireInternalSecret`;
its only gate is the Svix signature.

1. Cap the body (`http.MaxBytesReader`, 1 MiB) and read the **raw bytes** (re-marshaled JSON breaks the signature).
2. Verify with the standard library, no new dependency: signed content is `{svix-id}.{svix-timestamp}.{body}`;
   key is base64-decoded `RESEND_WEBHOOK_SECRET` minus the `whsec_` prefix; HMAC-SHA256; the
   `svix-signature` header is a space-separated list of `v1,<base64>` — accept if **any** matches, using
   `hmac.Equal`. Reject timestamps older/newer than 5 minutes.
3. Responses: bad/missing signature or headers → **400** + a `security_event` slog warn (never the body or secret);
   `RESEND_WEBHOOK_SECRET` unset → **503**; unknown `email_id` (not sent by notify, or the tiny race before
   `MarkSent` commits) → **200** and drop — the next event for the email carries the state anyway;
   DB error → **500** so Resend retries; everything else → **200**.
4. One transaction in a new `emailevents` package (owns its SQL, mirrors the store/handler layering):
   `INSERT email_events … ON CONFLICT (svix_id) DO NOTHING` (no row ⇒ duplicate ⇒ stop, 200) →
   conditional `UPDATE notification_deliveries … WHERE provider_status rank < new rank` →
   if the row changed **and** the new status is a problem state, insert the alert (§4.5).
   Any error rolls the whole thing back and returns 500, so a retried webhook cannot lose an alert.

The tenant is taken from the matched delivery row, **never** from the payload.

### 4.5 Sender alert

On the transition into a problem state, notify inserts a `notifications` row for the original
notification's `actor_user_id` (the control-plane identity id — what notify scopes by) plus an
already-terminal `in_app` delivery row:

- `event_type = "email.delivery_problem"`, `visible_in_app = true` (a problem alert is never suppressed by the in-app preference),
- `resource` / `resource_id` copied from the original, so the existing `accessible_resources` read filter
  shows the alert to anyone who could do the action that sent it,
- `title` / `body` from fixed client-safe templates (`"Email to {recipient} bounced"`, …) — **provider detail never goes into the alert**,
- `link` = the original notification's new optional `status_link` (the staff-side page where the badge lives). The
  create endpoint accepts an optional `statusLink` and stores it on the notification; absent ⇒ alert has no link.
  (The original `link` is not reused: for a customer document email it is the *customer's* download URL.)

No original actor (system-originated email) ⇒ no alert; the badge still updates. No email/push for the alert in v1 — bell only.

### 4.6 Status read endpoint

`POST /api/deliveries/email-status`, internal-secret gated (same `requireInternal` as the other service-to-service routes).

```jsonc
// request  (≤ 200 ids)
{ "tenantId": "…", "notificationIds": ["…"] }
// response
{ "success": true, "data": { "statuses": {
    "<notificationId>": { "state": "bounced", "recipient": "a@b.com",
                          "detail": "<provider text, server-side only>", "updatedAt": "…" } } } }
```

Query is `WHERE tenant_id = $1 AND notification_id = ANY($2) AND channel = 'email'` — a notification id from
another tenant yields nothing. Ids with no email row are simply absent from the map. The existing
`GET /api/notifications/{id}/deliveries` also gains `providerStatus`/`providerStatusAt` in its JSON.

### 4.7 Config

`RESEND_WEBHOOK_SECRET` — optional, like the other channel settings. Unset: boot logs a warning, the route
answers 503, nothing else changes.

## 5. StoneSuite-Backend

### 5.1 `services.EmailStatuses`

New file `services/notify_status.go` (the 300-line cap applies; `notify_delivery.go` is already ~280):
`EmailStatuses(ctx, tenantID, ids) (map[string]EmailDeliveryStatus, error)` — one batched POST to §4.6 using
`notifyClient` and the existing `X-Internal-Secret`. **Fail-open**: any error ⇒ log via `slog`, return an empty map; callers
render `unknown`, never a 500.

`FoldEmailStatus(statuses []EmailDeliveryStatus) EmailStatusSummary` reduces a send's recipients (to + cc) to the
**worst** state, severity order (worst first):
`bounced > failed > suppressed > complained > retrying > delayed > unknown > queued > sent > delivered > skipped`.
It also yields the per-recipient list for a tooltip. A fixed table maps each state to a **client-safe message**
(provider `detail` goes to `slog` only — the same rule `EmailOutcome.Detail` already follows).

### 5.2 Persist notify ids where they are missing

Already stored: `document_sends` (tenant), `user_invites` (control plane). Add
`notify_notification_ids TEXT[] NOT NULL DEFAULT '{}'` via `ADD COLUMN IF NOT EXISTS` (never in the `CREATE TABLE` body) to:

| Table | DB | Flow |
|---|---|---|
| `portal_invites` | control plane | staff-granted portal access (`portal_access.go`) |
| `tenant_invites` | control plane | platform onboarding invite/resend (`tenant.go`) |
| `customer_identities` | tenant | customer-portal invite (`customer_portal_admin.go`) |

PO "send to vendor" goes through `documents_deliver.go` → `document_sends`, so it is covered already.
Each flow writes the ids returned by its `Send…WithResult` call (the `user_invites` / `InsertDocumentSend` pattern).

### 5.3 Actor and `statusLink` on every email

`services/email.go` builders for portal, customer-portal and onboarding invites take no actor today. Thread the
sender's **identity id** through as `ActorUserID` wherever a user exists (`portal_access.go` already has
`actorIdentityID`). Platform-admin onboarding invites have no tenant-scoped actor ⇒ **badge only, no alert** (decision, §9).
Each flow also passes a `StatusLink` (staff-side page) on its `NotificationRequest`; absent ⇒ no link on the alert.

### 5.4 API shape

List/detail responses that already return these rows (document-send history, staff invites, portal invites,
customer-portal invites, platform tenant invites) gain, per row:

```jsonc
"emailStatus": "delivered",             // worst-of, §5.1 vocabulary
"emailStatusAt": "2026-10-01T12:00:00Z",
"emailStatusMessage": "…",              // client-safe; empty for sent/delivered
"emailRecipients": [{ "email": "a@b.com", "status": "delivered" }]
```

One `EmailStatuses` call per request (ids gathered across rows). Rows with no ids ⇒ `emailStatus: "unknown"`. New fields are
additive; `docs/openapi.json` is updated (the API-docs test guards it).

### 5.5 Docs

User-visible feature ⇒ update the matching end-user help doc (`docs/` embedded corpus) with the statuses and what to do
about a bounce; `TestHelpCorpusIsUserFacing` must stay green (no engineering content).

## 6. StoneSuite-WebUI

- `src/types/emailStatus.ts` + a shared `EmailStatusBadge` (text label **and** icon/colour — never colour alone; tooltip lists
  per-recipient status and the safe message).
- Used in: `SendToCustomerDialog` send history, `PurchaseOrderDetailPage`, `InviteDetail`, `PortalAccessPanel`, `TenantInvitesPanel`.
- Refresh on mount and on window focus. No websockets, no polling loop.
- The bell needs no change for the alert (it is an ordinary notification); verify it renders the new `event_type` generically.

## 7. Security

- Webhook: signature-only gate, constant-time compare, 5-minute replay window, body cap, no secret/body in logs,
  `security_event` on rejection. Tenant derived from our own row, never the payload.
- Status endpoint: internal secret + `tenant_id` predicate in the SQL; ids are opaque UUIDs.
- Provider text (can name the sending domain or an API-key problem) never reaches a client or an alert — slog only.
- IDOR: the backend only asks for ids it loaded from rows the caller is already authorized to read (`recordInScope` / the
  existing handler auth chain) — the status call adds no new access path.
- No new `log.Printf` in request paths; `slog` only.

## 8. Failure modes

| Failure | Behaviour |
|---|---|
| Webhook never arrives | Badge stays at `sent` (accurate: Resend accepted it). Resend retries non-2xx for ~a day. |
| Notify down when backend renders a list | `emailStatus: "unknown"`, page still loads. |
| Notify cold-starting (scale-to-zero) when Resend posts | First attempt wakes the Machine; Resend retries on failure. |
| Duplicate / replayed webhook | `svix_id` unique ⇒ no-op, no second alert. |
| Out-of-order events | Rank guard; only forward moves apply. |
| SMTP fallback in use / id not parsed | No webhook possible ⇒ shows `sent` and stays there. |
| Webhook for an email notify didn't send (shared Resend account) | 200 + drop. |

## 9. Decisions to confirm / open items

1. **Platform-admin onboarding invites: badge, no bell alert** (the admin isn't a user inside that tenant). *Defaulted; veto if wrong.*
2. **Alert visibility**: the alert copies the original `resource` so the existing `accessible_resources` filter applies. Verify with a test that a
   sender holding only their normal role actually sees it in the bell; if a flow's resource isn't in the sender's read grants, fall back to a dedicated resource.
3. **Staff-side `statusLink` URLs** per flow — chosen at plan time against the WebUI router.
4. **Not in v1:** opens/clicks, polling Resend's `GET /emails/{id}` to reconcile missed webhooks, forgot-password emails (must not reveal whether an account
   exists), fire-and-forget system notifications' badges (they still get tracked in notify), push/email for the alert itself.

## 10. Testing

- **notify:** Svix verification (valid, tampered body, wrong secret, stale/future timestamp, multi-signature header, missing headers);
  rank table-driven (in-order, out-of-order, equal-rank); duplicate `svix_id`; unknown `email_id`; alert fires exactly once per state and not for
  `sent`/`delivered`; no actor ⇒ no alert; `sendViaResend` parses `id` (and tolerates a bad body); `MarkSent` stores the id; status endpoint tenant isolation and 200-id cap.
  DB-backed tests where the existing notify tests use them.
- **backend:** `FoldEmailStatus` worst-of table-driven; `EmailStatuses` fail-open against an `httptest` notify; handler tests that list rows carry `emailStatus`;
  new columns round-trip; `module-drift-checker` not needed (no module added) but `tenancy-security-reviewer` on the touched handlers; `migration-auditor` on both `schema.sql` edits.
- **webui:** badge states + tooltip; each host component renders its status; fail-open "unknown" rendering.
- **End to end (after deploy):** send to `delivered@resend.dev`, `bounced@resend.dev` and `complained@resend.dev`; confirm badge transitions and that the bounce raises exactly one bell alert for the sender.

## 11. Rollout & operator checklist

Order matters; the backend and WebUI tolerate a missing status (`unknown`), so each step is independently safe.

1. **Deploy notify** (schema applies on boot). The route is live but answers 503 until the secret exists.
2. **Resend dashboard → Webhooks → Add endpoint** `https://stonesuite-notify.fly.dev/api/webhooks/resend`; events:
   `email.sent`, `email.delivered`, `email.delivery_delayed`, `email.bounced`, `email.complained`, `email.failed`, `email.suppressed`.
   Resend shows the signing secret (`whsec_…`) only once the endpoint exists.
3. **Set the secret immediately:** `fly secrets set RESEND_WEBHOOK_SECRET=whsec_… -a stonesuite-notify`. Events that hit the 503 window are
   retried by Resend, so nothing is lost.
4. **Deploy the backend** (new columns apply to every tenant DB on boot), then the **WebUI**.
5. Notify serves every environment (`NOTIFY_URL` is the one shared service), so there is exactly one webhook endpoint.

# Automation Engine — Claude rules

_Loaded automatically when Claude reads files under `automation/`. Moved verbatim from the root CLAUDE.md._

## Automation Engine (`automation/` + `POST /api/internal/automation/tick`)
The `automation-cron` Worker signs a tick every 15 min; `automation.Runner` runs every
`DefaultJobs()` entry per servable tenant **inside the request** (scale-to-zero can stop the
machine after the response).
0. **Platform on/off, default OFF** (`platform_automation_settings`, control plane;
   `GET/PUT /api/platform/automation`, platform-admin only). Every tick wakes the Fly
   machine and Neon, so the Worker reads a Cloudflare KV mirror (`automation_enabled`) and
   does not call the backend unless it is `"1"`; the tick re-checks the DB flag as a
   backstop and fails closed. The DB row is the source of truth, KV is a mirror: PUT writes
   DB first, then KV (502 + retry if KV fails). It also gates scheduled vendor-payment
   dispatch. Any new job inherits this; don't add a second scheduler path around it.
1. **"Today" is tenant-local, computed in Go** (`automation.TenantToday` /
   `TenantLocation`, from `company_profile.timezone`, fallback
   `AUTOMATION_DEFAULT_TIMEZONE`) and passed as a `$n` param. Automation SQL never uses
   `CURRENT_DATE`. External sends respect `InSendWindow` (08:00–18:00 local).
2. **Writes made by automation run under `automation.WithSystemActor(ctx)`**, so audit
   observers ignore them (loop guard: check `automation.IsSystemActor(ctx)` — an empty
   `AuditEvent.ActorUserID` does **not** mean automation; several human write paths pass
   `""`). Employee-keyed columns use
   `automation.SystemEmployeeID` (seeded employee 1). There is no automation `users` row;
   don't add one.
3. **Every outbound send carries `services.NotificationRequest.IdempotencyKey`** set to
   `sha256hex(occurrence key or digest key)` (always 64 bytes; notify caps keys at 128 and
   already scopes them per recipient, so never put the recipient in it), so a retry after a
   timeout can't double-send.
4. **Audit observers are a list** (`workflow.RegisterAuditObserver`). Each one runs
   panic-isolated, and none may block the write.
5. **AR dunning (`ar_dunning`, `automation/ar_*.go`)** emails one digest per customer per
   day for overdue/due-soon invoices. The rule is seeded **disabled**; tenants toggle it via
   `GET /api/tenant/automation/rules` (`automation:read`) and `PUT /api/tenant/automation/rules/{key}`
   (`automation:configure`, body `{"enabled":bool}`). Enabling stamps `automation_state.enabled_at`
   only on a disabled-to-enabled change (re-enable resets it, so no backlog blast); the rule
   flip and state write share one transaction. Each tick runs plan first (idempotent occurrence
   rows, window `due_date` in [today-60, today+3], stages never before their date), then `drain`
   (leases, guards, retry states). Caps and the tenant kill switch **defer** a digest (`planned`,
   `run_at` pushed) rather than skip it; rows go `stale` 48h after `created_at`. `unknown` sends
   count toward the weekly/daily caps. Disabling a rule skips its unsent rows (`rule_disabled`).
   Admin read endpoints: `GET /api/tenant/automation/deliveries` (delivery log, keyset
   pagination) and `POST /api/tenant/automation/rules/{key}/preview` (dry run, writes nothing).
   Deferred: owner in-app notes, stop-reminders link, `unknown` reconciliation, cadence edit API (P2).
6. **Expiry reminders (`quote_expiry`, `estimate_expiry`)** are seeded **disabled** and use a
   `-3d` cadence (3 days before expiry). They send owner-only **internal** digests through the
   drain's internal-recipient path, which honors only the tenant kill switch and ignores the
   external caps and suppression. Eligible documents are `SENT`, not converted, with an active
   user as owner.
7. **Stuck approvals (`approval_stuck`, job `approval_reminders`, `automation/approval_*.go`)** is seeded **disabled**
   with a `+2/+4/+6`-day cadence after the record entered its approval gate (max 3 reminders).
   Covers only notify-wired `approvalchain` registry modules (`DisplayName != ""`) and only
   gates whose `Reject != RejectInPlace`. Each unsigned live approver gets an **internal**
   digest, one per approver per day across all modules; when no unsigned live approver remains
   it falls back to the record owner plus active super admins. The clock restarts when the
   record re-enters the gate (new history row, new anchor). The internal path honours only the
   kill switch (plus the 08:00–18:00 send window), and the email links in-app (login required),
   not a public link. The preview endpoint does not support this rule.

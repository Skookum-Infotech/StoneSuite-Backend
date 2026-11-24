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
   day for overdue/due-soon invoices. All six system automation rules (`ar_dunning`, `quote_expiry`, `estimate_expiry`, `approval_stuck`, `install_tomorrow`, `anomaly_scan`) are seeded **enabled** with `enabled_at` stamped (a NULL `enabled_at` makes every stage `pre_enable` forever), so the platform switch is the only master gate; tenants can still toggle it via
   day for overdue/due-soon invoices. All six system automation rules (`ar_dunning`, `quote_expiry`, `estimate_expiry`, `approval_stuck`, `vendor_bill_due`, `anomaly_scan`) are seeded **enabled** with `enabled_at` stamped (a NULL `enabled_at` makes every stage `pre_enable` forever), so the platform switch is the only master gate; tenants can still toggle it via
   day for overdue/due-soon invoices. All six system automation rules (`ar_dunning`, `quote_expiry`, `estimate_expiry`, `approval_stuck`, `daily_digest`, `anomaly_scan`) are seeded **enabled** with `enabled_at` stamped (a NULL `enabled_at` makes every stage `pre_enable` forever), so the platform switch is the only master gate; tenants can still toggle it via
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
   `GET`/`PUT /api/tenant/automation/settings` (`automation:read` / `automation:configure`, body `{"killSwitch":bool}`) expose the tenant kill switch, read-only caps and the platform switch (`platformEnabled`, `null` when the control plane is unreadable); rules also return `isSystem`, `previewSupported` (`automation.PreviewRuleKey`) and `lastPlannedAt`, and deliveries a `recordNumber`.
   Turning `anomaly_scan` on is refused (409) while the platform switch is off, and fails closed (503) if the switch cannot be read; turning it off is always allowed.
   Deferred: owner in-app notes, stop-reminders link, `unknown` reconciliation, cadence edit API (P2).
6. **Expiry reminders (`quote_expiry`, `estimate_expiry`)** are seeded **enabled** and use a
   `-3d` cadence (3 days before expiry). They send owner-only **internal** digests through the
   drain's internal-recipient path, which honors only the tenant kill switch and ignores the
   external caps and suppression. Eligible documents are `SENT`, not converted, with an active
   user as owner.
7. **Stuck approvals (`approval_stuck`, job `approval_reminders`, `automation/approval_*.go`)** is seeded **enabled**
   with a `+2/+4/+6`-day cadence after the record entered its approval gate (max 3 reminders).
   Covers `approvalchain` registry modules that have a reminder label (`ModuleConfig.ReminderLabel()`: `DisplayName`, or `ReminderName` for `quote`, `estimate` and `sales_order`, which stay notification-dark) and only
   gates whose `Reject != RejectInPlace`. Each unsigned live approver gets an **internal**
   digest, one per approver per day across all modules; when no unsigned live approver remains
   it falls back to the record owner plus active super admins. The clock restarts when the
   record re-enters the gate (new history row, new anchor). The internal path honours only the
   kill switch (plus the 08:00–18:00 send window), and the email links in-app (login required),
   not a public link. The preview endpoint does not support this rule.
8. **Install-tomorrow reminders (`install_tomorrow`, job `install_reminders`, `automation/install_*.go`)** are seeded **enabled** (`enabled_at` stamped) with a `-1d` cadence relative to `install_appointment.local_start_date`. One delivery row per visit × crew member (occurrence key carries the user id, so a crew change only adds rows for newcomers); recipients are crew whose employee resolves to an active user, via the internal drain path (kill switch + 08:00–18:00 window only). Candidates are `scheduled` visits on live, non-`CANC` jobs dated today..today+2 (a missed stage is a catch-up send worded "today"; visits already past are never reminded). The renderer re-checks live and drops `cancelled`, `completed`, `voided`, `anchor_changed` (rescheduled), `crew_changed`, `no_email`. The email links in-app (login required). The preview endpoint does not support this rule.
9. **Vendor bill due reminders (`vendor_bill_due`, job `vendor_bill_reminders`, `automation/vendorbill_*.go`)** are seeded **enabled** (`enabled_at` stamped) with a `-3/0`-day cadence relative to `vendor_bill_due_date`. Owner-only **internal** digests (kill switch + 08:00–18:00 window only). Candidates are `APPV`/`PART`/`ODUE` bills with a live balance (`grand_total - amount_paid - credit_total > 0`) due in [today-2, today+3]; `PINI` is excluded (payment in flight). The renderer re-checks live and drops `voided`, `paid`, `payment_initiated`, `not_payable`, `anchor_changed`, `owner_changed`, `no_email`. The email links in-app (login required). The preview endpoint does not support this rule.
8. **Daily digest (`daily_digest`, job `daily_digest`, `automation/digest_*.go`)** is seeded **enabled** (`enabled_at` stamped) but sends nothing until a user opts in: preferences live in `automation_digest_prefs` (no row = off; `send_hour` 8..17 tenant-local, per-section flags, `skip_empty`), read/written by the caller only via `GET`/`PUT /api/tenant/automation/digest/me` (self-service like dashboard `widgets/me`: login + tenant, no id in the request, no role permission). It has no cadence (not in `editableCadences`). One **internal** delivery per opted-in user per tenant-local day (occurrence key carries the date), planned on the first tick at/after the user's `send_hour` while the 08:00–18:00 window is open. The renderer re-reads prefs and content live and drops `digest_off`, `recipient_changed`, `empty`; sections are the user's own approvals (`approvalinbox.Count`, scope `mine`), overdue invoices and quotes/estimates expiring in 7 days (owned via `employee_user_id`). The preview endpoint does not support this rule.

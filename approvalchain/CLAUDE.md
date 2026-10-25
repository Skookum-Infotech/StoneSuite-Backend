# Notifications — Claude rules

_Loaded automatically when Claude reads files under `approvalchain/`. Moved verbatim from the root CLAUDE.md._

## Notifications (`approvalchain/` + `services/notify.go` + `stonesuite-notify`)
Approval/transition notifications route through the shared `approvalchain` engine to a
separate deployed service, `stonesuite-notify` (its own repo, shares this app's
`JWT_SECRET` so a StoneSuite JWT authenticates against both).
1. **A module is notification-dead until it's registered in `approvalchain/registry.go`.**
   Every `Notify*` helper in `approvalchain/notify.go` (`NotifyApprovalRequested`,
   `NotifyApprovalRejected`) and the shared `engine.Approve()` no-op immediately when
   `ModuleConfig.DisplayName == ""` (`approvalchain/notify.go:55,86,128`) — this is the
   guard `registry_test.go` checks against. Scaffolding a module without filling in
   `DisplayName`/`Resource`/`OwnerColumn`/`NumberColumn` on its `Record`/`ModuleConfig`
   silently ships approvals with zero notifications; same clone-discipline trap as the
   module system itself, and just as easy to miss in review.
2. **Every transition handler must call a `notifyTransition` hook after a successful
   status change** — see `invoice/store_transition.go` for the reference pattern (compute
   `requiredHere`/`targetApprovers` via `approvalchain.ActiveApproverCount`, then call
   `approvalchain.NotifyApprovalRequested`/`NotifyApprovalRejected` with an `EventContext`).
   Approve/reject already fire from the shared engine; transition does not, so hand-port
   this call to every new module the same way as auth/scope/logging.
3. **Set `ActorUserID` on every `services.NotificationRequest`** you build outside the
   engine (e.g. document-send in `controllers/documents.go`, workspace invites in
   `controllers/user.go` via `services.SendUserInviteEmailWithResult`) — omitting it means
   Notify can't tell who performed the action, which breaks any "you just did this"
   notification addressed back to the actor.
4. **Don't inline notify HTTP calls into `controllers/`** — go through `services/notify.go`'s
   existing client.
5. **A handler that sends a transactional email must surface the outcome, not swallow it.**
   Return `"emailSent": bool` in the JSON response and log failures via `slog` (never a new
   `log.Printf` — CI guards this in the request-path packages). Best-effort delivery is fine
   (the invite/reset link is still returned), but a silent "sent" when Notify or its
   downstream Resend is failing is the bug that keeps recurring. Persist the returned notify
   ids on the domain row (`SendNotificationWithResult` + a `notify_notification_ids TEXT[]`
   column) so a "did it actually arrive?" reconciliation is possible — `controllers/user.go`
   `InviteUser` and `controllers/documents.go` `Send` are the reference.
6. **This backend never talks to Resend — inbound included.** A customer's reply to a record
   email (Communication tab) is received by Resend, whose `email.received` event goes to
   `stonesuite-notify`'s `POST /api/webhooks/resend`; notify fetches the mail and forwards it to
   `POST /api/internal/email/inbound` (HMAC-signed with `NOTIFY_API_KEY`, verified by
   `middleware.VerifyInternalSignature` + `ReplayGuard`). Don't add `RESEND_API_KEY`, a Resend
   SDK/HTTP client or a Svix verifier here. `REPLY_EMAIL_DOMAIN` must equal notify's value —
   notify uses it to decide which received mail to forward. Files attached to a reply follow the
   same rule: the mail route only records their metadata (and answers with `uploadRef` + `pending`),
   then notify sends each file to `PUT /api/internal/email/inbound/{ref}/attachments/{id}`
   (signed, raw bytes, `recordemail` limits) and the backend copies it into the tenant's R2 bucket
   (`record_email_attachment`). Staff attachments on a reply are multipart, 5 MB total
   (`recordemail.ReplyMaxTotalBytes`, mirrored in the web app's `lib/recordEmail.ts`).

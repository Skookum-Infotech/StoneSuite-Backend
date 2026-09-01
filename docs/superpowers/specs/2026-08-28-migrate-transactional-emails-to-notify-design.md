# Migrate Remaining Transactional Emails to Notify — Design Spec

**Date:** 2026-08-28 · **Status:** Approved; ready for implementation planning
**Author:** Backend architecture pass (Claude)
**Repo affected:** `StoneSuite-Backend` only. `stonesuite-notify` needs no changes — the
email-only-recipient support this design depends on (`recipientUserId` OR `recipientEmail`,
`EmailBodyHTML` override, no in-app/push for email-only recipients) already shipped for the
document-send migration (`2026-08-28-notify-customer-document-email-design.md`).

**Completes:** `2026-08-28-notify-customer-document-email-design.md` §7 ("Phase 2, separate
spec") — migrating the standalone transactional emails in `services/email.go` that spec
deliberately deferred.

## 1. Problem

`services/email.go` has 7 functions that still send email directly via Resend → SMTP → no-op
fallback (`sendEmail`), bypassing Notify entirely: no queue, no retry, no delivery log, no
audit trail beyond whatever the caller logs on failure. Only the document-send customer email
and the two `notifyOwnerOfSend`/`notifyCustomerWelcome` pings go through Notify today.

The 7 un-migrated functions, and every call site:

| Function | Call sites |
|---|---|
| `SendOnboardingInviteEmail` | `controllers/tenant.go:412`, `:677` |
| `SendPasswordSetupEmail` | `controllers/onboarding_flow.go:82` |
| `SendUserInviteEmail` | `controllers/user.go:219`, `:498` |
| `SendPasswordResetEmail` | `controllers/portal_auth.go:579`, `controllers/tenant.go:1244` |
| `SendPortalInviteEmail` | `controllers/portal_access.go:254` |
| `SendCustomerPortalInviteEmail` | `controllers/customer_portal_admin.go:122` |
| `SendCustomerNoteConfirmationEmail` | `controllers/customer_portal.go:89` |

The blocker the original Phase 1 spec flagged — "each of these has its own question of whether
a real `tenantId`/`recipientUserId` is available" — is resolved here: `tenantId` is available
at every site (directly or via `identity.TenantID`), and none of these recipients has a real
tenant-scoped `users.id` (they're pre-account invitees or control-plane `identities`, not
`users` rows), so all 7 use **email-only recipients**, exactly like the document-send customer
copy already does.

## 2. Decisions

| # | Decision | Choice |
|---|---|---|
| D1 | New Notify plumbing needed? | **None.** `recipientEmail`-only support, `EmailBodyHTML` override, and fixed `{EmailEnabled:true, InAppEnabled:false, PushEnabled:false}` preferences for email-only recipients already exist in `stonesuite-notify` (verified in `notifications/types.go` and `controllers/notifications.go` on `develop`). |
| D2 | How does each function get a `tenantId`/`ctx`? | **Add `ctx context.Context, tenantID string` as the first two parameters** to each of the 7 functions. Every call site already has both in scope (see §3 table) — no new lookups. |
| D3 | Recipient identification | **Email-only** (`RecipientTarget{Email: ...}`, no `UserID`) for all 7 — consistent with D1's premise; none of these recipients has a real `users.id`. |
| D4 | Email content/branding | **Unchanged.** Each function keeps building its existing HTML string exactly as today; that string becomes `EmailBodyHTML` on the `NotificationRequest` instead of the argument to `sendEmail`. |
| D5 | Failure behavior | **Unchanged at the call site: still best-effort, still just logged.** The function itself now only returns an error if Notify is unreachable/rejects the request outright (mirrors `documents.go`'s `502` handling) — once Notify accepts (`201`), delivery is async with retries. Same accepted trade-off as the document-send migration (that spec's §5); no caller here treats email failure as fatal today, so this is not a regression. |
| D6 | Fate of `sendEmail`/`sendViaResend`/`sendViaSMTP` | **Delete once the 7th caller stops using them** — dead code after this change, same precedent as `SendDocumentEmail`'s removal in Phase 1. The `net/smtp` import and the Resend HTTP-call code in `services/email.go` go with it. |
| D7 | `RESEND_API_KEY`/`SMTP_HOST`/`SENDER_EMAIL`/`SENDER_PASSWORD` config | **Leave in `config/config.go` and Fly secrets for this change** — flagged as a follow-up, not removed here, since they may still be referenced in ops runbooks/docs outside this repo's grep reach. |

## 3. Per-function mapping

Verified directly against each call site's surrounding code (variable names as they exist in
the current `develop` branch):

| Function | `tenantID` source | `resourceId` source | `eventType` | `resource` |
|---|---|---|---|---|
| `SendOnboardingInviteEmail` | `tenant.ID` | `invite.ID` | `tenant.onboarding_invited` | `tenant` |
| `SendPasswordSetupEmail` | `tenant.ID` | `identity.ID` | `identity.password_setup` | `identity` |
| `SendUserInviteEmail` | `tenant.ID` | `invite.ID` | `user.invited` | `user` |
| `SendPasswordResetEmail` | `identity.TenantID` (no separate `tenant` var in scope at either call site) | `identity.ID` | `identity.password_reset` | `identity` |
| `SendPortalInviteEmail` | `tenant.ID` | `invite.ID` | `portal_user.invited` | `portal_user` |
| `SendCustomerPortalInviteEmail` | `tenant.ID` | `custInternalID` | `customer_portal.invited` | `customer_portal` |
| `SendCustomerNoteConfirmationEmail` | `tenant.ID` (via `tenancy.TenantFromContext`) | `note.ID` | `customer_note.confirmed` | `customer_note` |

`Title` is each function's existing `subject` string; `Body` is a short fixed audit string
(e.g. `"Password reset email sent."`) since the real content is `EmailBodyHTML`, mirroring
`documents.go`'s `Body: "Document sent."` pattern.

## 4. Design — function shape

Every migrated function follows the same shape (illustrated with `SendPasswordResetEmail`):

```go
func SendPasswordResetEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, resetLink string) error {
	subject := "Reset your StoneSuite password"
	body := fmt.Sprintf(`...same HTML as today...`, nameClause(recipientName), resetLink, resetLink)
	return SendNotification(ctx, NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "identity.password_reset",
		Resource:      "identity",
		ResourceID:    identityID,
		Title:         subject,
		Body:          "Password reset email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	})
}
```

Call sites change from (e.g., `portal_auth.go:579`):
```go
if err := services.SendPasswordResetEmail(identity.Email, identity.FullName, portalResetLink(token)); err != nil {
```
to:
```go
if err := services.SendPasswordResetEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, identity.FullName, portalResetLink(token)); err != nil {
```
— a mechanical two-argument addition, no control-flow change (still logged, not fatal).

## 5. Cleanup

Once all 7 call sites are migrated, `services/email.go` no longer calls `sendEmail`. Delete:
- `sendEmail`, `sendViaResend`, `sendViaSMTP`, and their doc comments
- the `net/smtp`, `bytes`, `encoding/json` (if otherwise unused), `net/http` imports they alone required — re-check each import's remaining usage before removing
- nothing else in the file changes; the 7 `SendXEmail` functions and `nameClause` stay

## 6. Testing

- Update each of the 7 functions' direct tests (if any exist in `services/email_test.go`) to
  assert against a mocked `SendNotification` instead of Resend/SMTP behavior, same pattern
  `documents_test.go` already uses for the Phase 1 migration.
- Update each call site's controller test (`tenant_test.go`, `onboarding_flow_test.go`,
  `user_test.go`, `portal_auth_test.go`, `portal_access_test.go`,
  `customer_portal_admin_test.go`, `customer_portal_test.go` — whichever exist) wherever they
  currently assert on the old function signature or mock Resend/SMTP.
- No `stonesuite-notify` test changes — no code changes there.

## 7. Out of scope

- Any change to `stonesuite-notify` (already supports everything this needs).
- Removing `RESEND_API_KEY`/`SMTP_*` config (D7) — follow-up.
- A frontend delivery-status UI for these emails — same as Phase 1, no frontend surfaces
  Notify's delivery log today; not introduced here.

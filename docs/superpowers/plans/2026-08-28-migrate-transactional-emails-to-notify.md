# Migrate Transactional Emails to Notify Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route the 7 remaining direct Resend/SMTP transactional email functions in `services/email.go` through `stonesuite-notify` (queue/retry/audit), matching the pattern already shipped for the document-send customer email.

**Architecture:** Each of the 7 `SendXEmail` functions gains `ctx context.Context, tenantID string` (plus a resource-id parameter) as its first arguments, keeps building the exact same HTML string it does today, and passes that string as `EmailBodyHTML` on a `NotificationRequest` sent via the existing `SendNotification` (in `services/notify.go` — no changes needed there). Each function's HTML-building is split into a small pure `build<X>Notification(...) NotificationRequest` helper so it's unit-testable without HTTP mocking, mirroring `controllers/documents.go`'s existing `customerSendRequest` helper. All 7 use email-only recipients (`RecipientTarget{Email: ...}`, no `UserID`) since none of these flows has a real tenant-scoped `users.id` on hand.

**Tech Stack:** Go, testify (`assert`/`require`), `stonesuite-backend/services` package.

## Global Constraints

- Recipient identification for all 7 functions: email-only (`RecipientTarget{Email: recipientEmail}`, `UserID` left empty) — no tenant-scoped `users.id` is available at any of these call sites.
- Email content/branding is unchanged — each function's existing HTML string becomes `EmailBodyHTML` verbatim; `Body` on the `NotificationRequest` is a short fixed audit string (real content is `EmailBodyHTML`), same pattern as `controllers/documents.go`'s `customerSendRequest`.
- `Channels: []string{"email"}` on every request (matches the document-send precedent).
- Call sites keep treating email failure as best-effort (log and continue, not fatal) — no change to that control flow, only to what's inside the function being called.
- No changes to `stonesuite-notify` — it already supports email-only recipients (verified on `develop`).
- Every migrated function must still return the same underlying error type it does today (`error` from `SendNotification`) so existing `if err := services.SendXEmail(...); err != nil { log.Printf(...) }` call sites need no logic changes beyond the added arguments.

---

## Task 1: Migrate `SendOnboardingInviteEmail`

**Files:**
- Create: `services/email_test.go`
- Modify: `services/email.go:1-14` (imports), `services/email.go:16-35` (function body)
- Modify: `controllers/tenant.go:412`, `controllers/tenant.go:677`
- Test: `services/email_test.go`

**Interfaces:**
- Consumes: `services.NotificationRequest`, `services.RecipientTarget` (already defined in `services/notify.go`) — `TenantID string`, `Recipients []RecipientTarget{{Email string}}`, `EventType string`, `Resource string`, `ResourceID string`, `Title string`, `Body string`, `EmailBodyHTML string`, `Channels []string`.
- Produces: `services.SendOnboardingInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, inviteLink string) error` — new signature, used by later controller edits in this task only.

- [ ] **Step 1: Write the failing test**

Create `services/email_test.go`:

```go
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildOnboardingInviteNotification(t *testing.T) {
	req := buildOnboardingInviteNotification("tenant-1", "invite-1", "owner@example.com", "Jane Owner", "https://app.example/apply?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "owner@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "tenant.onboarding_invited", req.EventType)
	assert.Equal(t, "tenant", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "Your StoneSuite Onboarding Invitation", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Jane Owner")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/apply?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildOnboardingInviteNotification -v`
Expected: FAIL with `undefined: buildOnboardingInviteNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace the import block (`services/email.go:1-14`):

```go
package services

import (
	"context"
	"fmt"
)
```

(Leave the rest of the file's other functions untouched for now — later tasks in this plan remove `sendViaResend`/`sendViaSMTP`/`sendEmail` and their now-unused imports in Task 8, once nothing calls them.)

Replace `SendOnboardingInviteEmail` (`services/email.go:16-35`):

```go
// buildOnboardingInviteNotification builds the Notify request for a tenant
// onboarding invite email.
func buildOnboardingInviteNotification(tenantID, inviteID, recipientEmail, recipientName, inviteLink string) NotificationRequest {
	subject := "Your StoneSuite Onboarding Invitation"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>You're invited to join StoneSuite</h2>
			<p>Hello %s,</p>
			<p>You've been invited to complete an onboarding experience with StoneSuite.</p>
			<p>To begin your onboarding, click the link below:</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Start Onboarding</a></p>
			<p>If the button does not work, copy and paste this link into your browser:</p>
			<p>%s</p>
			<p>This invitation link is time-limited for security.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, recipientName, inviteLink, inviteLink)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "tenant.onboarding_invited",
		Resource:      "tenant",
		ResourceID:    inviteID,
		Title:         subject,
		Body:          "Onboarding invite email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendOnboardingInviteEmail sends an invitation email for customer onboarding.
func SendOnboardingInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, inviteLink string) error {
	return SendNotification(ctx, buildOnboardingInviteNotification(tenantID, inviteID, recipientEmail, recipientName, inviteLink))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildOnboardingInviteNotification -v`
Expected: PASS

- [ ] **Step 5: Update call sites**

In `controllers/tenant.go:412`, change:
```go
	emailErr := services.SendOnboardingInviteEmail(req.ContactEmail, req.RecipientName, link)
```
to:
```go
	emailErr := services.SendOnboardingInviteEmail(r.Context(), tenant.ID, invite.ID, req.ContactEmail, req.RecipientName, link)
```

In `controllers/tenant.go:677`, change:
```go
		emailErr := services.SendOnboardingInviteEmail(contactEmail, tenant.DisplayName, link)
```
to:
```go
		emailErr := services.SendOnboardingInviteEmail(r.Context(), tenant.ID, invite.ID, contactEmail, tenant.DisplayName, link)
```

(This is inside `tenantInvites`, `controllers/tenant.go:615`, whose `POST` branch resolves `tenant, err := h.CP.TenantByID(r.Context(), tenantID)` at `controllers/tenant.go:630` — both `tenant.ID` and `invite.ID` — from `var invite *tenancy.Invite` assigned at `controllers/tenant.go:662-667` — are in scope at line 677.)

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/tenant.go
git commit -m "feat(notify): route onboarding invite email through Notify"
```

---

## Task 2: Migrate `SendPasswordSetupEmail`

**Files:**
- Modify: `services/email.go` (add function + builder after the onboarding-invite pair from Task 1)
- Modify: `controllers/onboarding_flow.go:82`
- Test: `services/email_test.go`

**Interfaces:**
- Consumes: same `NotificationRequest`/`RecipientTarget` shape as Task 1.
- Produces: `services.SendPasswordSetupEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, setupLink string) error`.

- [ ] **Step 1: Write the failing test**

Append to `services/email_test.go`:

```go
func TestBuildPasswordSetupNotification(t *testing.T) {
	req := buildPasswordSetupNotification("tenant-1", "identity-1", "customer@example.com", "Sam Customer", "https://app.example/set-password?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "customer@example.com", req.Recipients[0].Email)
	assert.Equal(t, "identity.password_setup", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Set up your StoneSuite account", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam Customer")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildPasswordSetupNotification -v`
Expected: FAIL with `undefined: buildPasswordSetupNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace `SendPasswordSetupEmail`:

```go
// buildPasswordSetupNotification builds the Notify request for a
// post-approval "set your password" email.
func buildPasswordSetupNotification(tenantID, identityID, recipientEmail, recipientName, setupLink string) NotificationRequest {
	subject := "Set up your StoneSuite account"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Your StoneSuite workspace is ready</h2>
			<p>Hello %s,</p>
			<p>Your onboarding has been approved and your workspace is being set up.</p>
			<p>Set your password to finish activating your account:</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Set Password</a></p>
			<p>If the button does not work, copy and paste this link into your browser:</p>
			<p>%s</p>
			<p>This link is time-limited for security.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, recipientName, setupLink, setupLink)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "identity.password_setup",
		Resource:      "identity",
		ResourceID:    identityID,
		Title:         subject,
		Body:          "Password setup email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendPasswordSetupEmail sends the "set your password" email after a customer's
// onboarding application is approved (or they are onboarded directly).
func SendPasswordSetupEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, setupLink string) error {
	return SendNotification(ctx, buildPasswordSetupNotification(tenantID, identityID, recipientEmail, recipientName, setupLink))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildPasswordSetupNotification -v`
Expected: PASS

- [ ] **Step 5: Update call site**

In `controllers/onboarding_flow.go:82`, change:
```go
	if err := services.SendPasswordSetupEmail(email, fullName, link); err != nil {
```
to:
```go
	if err := services.SendPasswordSetupEmail(ctx, tenant.ID, identity.ID, email, fullName, link); err != nil {
```

(`finalizeOnboarding`'s own signature, `func (h *TenantOps) finalizeOnboarding(ctx context.Context, tenant *tenancy.Tenant, formData map[string]any) (string, error)`, already has `ctx` and `tenant.ID` in scope; `identity` is created earlier in the same function at `services/../controllers/onboarding_flow.go:42` via `h.CP.CreateIdentity(...)`.)

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/onboarding_flow.go
git commit -m "feat(notify): route password-setup email through Notify"
```

---

## Task 3: Migrate `SendUserInviteEmail`

**Files:**
- Modify: `services/email.go`
- Modify: `controllers/user.go:219`, `controllers/user.go:498`
- Test: `services/email_test.go`

**Interfaces:**
- Produces: `services.SendUserInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, workspaceName, inviteLink string) error`.

- [ ] **Step 1: Write the failing test**

Append to `services/email_test.go`:

```go
func TestBuildUserInviteNotification(t *testing.T) {
	req := buildUserInviteNotification("tenant-1", "invite-1", "colleague@example.com", "Alex Colleague", "Acme Stone Co", "https://app.example/accept?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "colleague@example.com", req.Recipients[0].Email)
	assert.Equal(t, "user.invited", req.EventType)
	assert.Equal(t, "user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "You've been invited to Acme Stone Co", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Alex Colleague")
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/accept?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildUserInviteNotification -v`
Expected: FAIL with `undefined: buildUserInviteNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace `SendUserInviteEmail`:

```go
// buildUserInviteNotification builds the Notify request for a colleague
// workspace invite email.
func buildUserInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, inviteLink string) NotificationRequest {
	subject := "You've been invited to " + workspaceName
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>You're invited to join %s</h2>
			<p>Hello%s,</p>
			<p>A colleague has invited you to join the <strong>%s</strong> workspace on StoneSuite.</p>
			<p>Click the link below to accept your invitation and set your password:</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Accept Invitation</a></p>
			<p>If the button does not work, copy and paste this link into your browser:</p>
			<p>%s</p>
			<p>This invitation expires in 48 hours. If you did not expect this email, you can safely ignore it.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, workspaceName, nameClause(recipientName), workspaceName, inviteLink, inviteLink)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "user.invited",
		Resource:      "user",
		ResourceID:    inviteID,
		Title:         subject,
		Body:          "User invite email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendUserInviteEmail sends an email to a colleague invited to join a tenant workspace.
func SendUserInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, workspaceName, inviteLink string) error {
	return SendNotification(ctx, buildUserInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, inviteLink))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildUserInviteNotification -v`
Expected: PASS

- [ ] **Step 5: Update call sites**

In `controllers/user.go:219`, change:
```go
	if err := services.SendUserInviteEmail(req.Email, req.FullName, tenant.DisplayName, link); err != nil {
```
to:
```go
	if err := services.SendUserInviteEmail(r.Context(), tenant.ID, invite.ID, req.Email, req.FullName, tenant.DisplayName, link); err != nil {
```

In `controllers/user.go:498`, change:
```go
	if err := services.SendUserInviteEmail(refreshed.Email, refreshed.FullName, tenant.DisplayName, link); err != nil {
```
to:
```go
	if err := services.SendUserInviteEmail(r.Context(), tenant.ID, refreshed.ID, refreshed.Email, refreshed.FullName, tenant.DisplayName, link); err != nil {
```

(`refreshed` is the `*tenancy.UserInvite` returned by `h.CP.RefreshUserInvite(...)` two lines above at `controllers/user.go:491` — it has an `ID` field.)

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/user.go
git commit -m "feat(notify): route user invite email through Notify"
```

---

## Task 4: Migrate `SendPasswordResetEmail`

**Files:**
- Modify: `services/email.go`
- Modify: `controllers/portal_auth.go:579`, `controllers/tenant.go:1244`
- Test: `services/email_test.go`

**Interfaces:**
- Produces: `services.SendPasswordResetEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, resetLink string) error`.

- [ ] **Step 1: Write the failing test**

Append to `services/email_test.go`:

```go
func TestBuildPasswordResetNotification(t *testing.T) {
	req := buildPasswordResetNotification("tenant-1", "identity-1", "user@example.com", "Sam User", "https://app.example/reset?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "user@example.com", req.Recipients[0].Email)
	assert.Equal(t, "identity.password_reset", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Reset your StoneSuite password", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam User")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/reset?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildPasswordResetNotification -v`
Expected: FAIL with `undefined: buildPasswordResetNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace `SendPasswordResetEmail`:

```go
// buildPasswordResetNotification builds the Notify request for a
// forgot-password reset-link email.
func buildPasswordResetNotification(tenantID, identityID, recipientEmail, recipientName, resetLink string) NotificationRequest {
	subject := "Reset your StoneSuite password"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Reset your password</h2>
			<p>Hello%s,</p>
			<p>We received a request to reset the password for your StoneSuite account.</p>
			<p>Click the link below to choose a new password (expires in 1 hour):</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Reset Password</a></p>
			<p>If the button does not work, copy and paste this link into your browser:</p>
			<p>%s</p>
			<p>If you did not request a password reset, you can safely ignore this email — your password will not change.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, nameClause(recipientName), resetLink, resetLink)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "identity.password_reset",
		Resource:      "identity",
		ResourceID:    identityID,
		Title:         subject,
		Body:          "Password reset email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

func SendPasswordResetEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, resetLink string) error {
	return SendNotification(ctx, buildPasswordResetNotification(tenantID, identityID, recipientEmail, recipientName, resetLink))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildPasswordResetNotification -v`
Expected: PASS

- [ ] **Step 5: Update call sites**

In `controllers/portal_auth.go:579`, change:
```go
	if err := services.SendPasswordResetEmail(identity.Email, identity.FullName,
		portalResetLink(token)); err != nil {
```
to:
```go
	if err := services.SendPasswordResetEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, identity.FullName,
		portalResetLink(token)); err != nil {
```

In `controllers/tenant.go:1244`, change:
```go
	if err := services.SendPasswordResetEmail(identity.Email, identity.FullName, link); err != nil {
```
to:
```go
	if err := services.SendPasswordResetEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, identity.FullName, link); err != nil {
```

(Both sites resolve `identity` via `h.CP.IdentityByEmail(...)` a few lines above, which returns `*tenancy.Identity` — it has both `ID` and `TenantID` fields. Neither handler has a separate `tenant` variable in scope, so `identity.TenantID` is the source, not `tenant.ID`.)

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/portal_auth.go controllers/tenant.go
git commit -m "feat(notify): route password-reset email through Notify"
```

---

## Task 5: Migrate `SendPortalInviteEmail`

**Files:**
- Modify: `services/email.go`
- Modify: `controllers/portal_access.go:254`
- Test: `services/email_test.go`

**Interfaces:**
- Produces: `services.SendPortalInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) error`.

- [ ] **Step 1: Write the failing test**

Append to `services/email_test.go`:

```go
func TestBuildPortalInviteNotification(t *testing.T) {
	req := buildPortalInviteNotification("tenant-1", "invite-1", "staffer@example.com", "Pat Staffer", "Acme Stone Co", "https://app.example/portal-setup?token=abc", 72)

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "staffer@example.com", req.Recipients[0].Email)
	assert.Equal(t, "portal_user.invited", req.EventType)
	assert.Equal(t, "portal_user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "Acme Stone Co — set up your customer portal access", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Pat Staffer")
	assert.Contains(t, req.EmailBodyHTML, "72 hours")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/portal-setup?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildPortalInviteNotification -v`
Expected: FAIL with `undefined: buildPortalInviteNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace `SendPortalInviteEmail`:

```go
// buildPortalInviteNotification builds the Notify request for an approved
// customer's portal-login setup invite.
func buildPortalInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) NotificationRequest {
	subject := workspaceName + " — set up your customer portal access"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Your %s customer portal</h2>
			<p>Hello%s,</p>
			<p><strong>%s</strong> has given you access to their customer portal, where you can
			   view your sales orders, invoices, payments and refunds at any time.</p>
			<p>Click below to set your password and sign in:</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Set up my access</a></p>
			<p>If the button does not work, copy and paste this link into your browser:</p>
			<p>%s</p>
			<p>This link expires in %d hours. If you were not expecting this email, you can safely ignore it.</p>
			<p>Best regards,<br>%s</p>
		</body>
		</html>
	`, workspaceName, nameClause(recipientName), workspaceName,
		setupLink, setupLink, expiryHours, workspaceName)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "portal_user.invited",
		Resource:      "portal_user",
		ResourceID:    inviteID,
		Title:         subject,
		Body:          "Portal invite email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendPortalInviteEmail invites an approved customer to set up their portal
// login. Distinct from SendUserInviteEmail: the recipient is a customer, not a
// colleague joining the workspace, so the copy must not imply staff access.
func SendPortalInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) error {
	return SendNotification(ctx, buildPortalInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink, expiryHours))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildPortalInviteNotification -v`
Expected: PASS

- [ ] **Step 5: Update call site**

In `controllers/portal_access.go:254`, change:
```go
	if merr := services.SendPortalInviteEmail(email, fullName, tenant.DisplayName,
		portalInviteLink(token), inviteExpiryHours()); merr != nil {
```
to:
```go
	if merr := services.SendPortalInviteEmail(r.Context(), tenant.ID, invite.ID, email, fullName, tenant.DisplayName,
		portalInviteLink(token), inviteExpiryHours()); merr != nil {
```

(This is inside `issueInvite(r *http.Request, tenant *tenancy.Tenant, identityID, email, fullName, customerUUID, actorIdentityID string) (*tenancy.PortalInvite, error)`, `controllers/portal_access.go:229-230` — `tenant.ID` is its parameter, `invite` is the `*tenancy.PortalInvite` assigned at `controllers/portal_access.go:239-249` via `h.CP.RefreshPortalInvite`/`h.CP.CreatePortalInvite`.)

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/portal_access.go
git commit -m "feat(notify): route portal invite email through Notify"
```

---

## Task 6: Migrate `SendCustomerPortalInviteEmail`

**Files:**
- Modify: `services/email.go`
- Modify: `controllers/customer_portal_admin.go:1-17` (imports), `controllers/customer_portal_admin.go:122`
- Test: `services/email_test.go`

**Interfaces:**
- Produces: `services.SendCustomerPortalInviteEmail(ctx context.Context, tenantID, resourceID, recipientEmail, recipientName, tenantDisplayName, setupLink string) error`.

- [ ] **Step 1: Write the failing test**

Append to `services/email_test.go`:

```go
func TestBuildCustomerPortalInviteNotification(t *testing.T) {
	req := buildCustomerPortalInviteNotification("tenant-1", "42", "buyer@example.com", "Casey Buyer", "Acme Stone Co", "https://portal.example/set-password?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Equal(t, "customer_portal.invited", req.EventType)
	assert.Equal(t, "customer_portal", req.Resource)
	assert.Equal(t, "42", req.ResourceID)
	assert.Equal(t, "You've been invited to the Acme Stone Co customer portal", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Casey Buyer")
	assert.Contains(t, req.EmailBodyHTML, "https://portal.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildCustomerPortalInviteNotification -v`
Expected: FAIL with `undefined: buildCustomerPortalInviteNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace `SendCustomerPortalInviteEmail`:

```go
// buildCustomerPortalInviteNotification builds the Notify request for an
// external customer's portal-login setup invite.
func buildCustomerPortalInviteNotification(tenantID, resourceID, recipientEmail, recipientName, tenantDisplayName, setupLink string) NotificationRequest {
	subject := "You've been invited to the " + tenantDisplayName + " customer portal"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>You're invited to the %s customer portal</h2>
			<p>Hello%s,</p>
			<p><strong>%s</strong> has invited you to their customer portal, where you can submit notes and questions directly to their team.</p>
			<p>Click the link below to set your password and activate your account:</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Set Password</a></p>
			<p>If the button does not work, copy and paste this link into your browser:</p>
			<p>%s</p>
			<p>This invitation link is time-limited for security.</p>
			<p>Best regards,<br>%s</p>
		</body>
		</html>
	`, tenantDisplayName, nameClause(recipientName), tenantDisplayName, setupLink, setupLink, tenantDisplayName)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "customer_portal.invited",
		Resource:      "customer_portal",
		ResourceID:    resourceID,
		Title:         subject,
		Body:          "Customer portal invite email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendCustomerPortalInviteEmail invites an external customer to set a
// password and activate their customer-portal login.
func SendCustomerPortalInviteEmail(ctx context.Context, tenantID, resourceID, recipientEmail, recipientName, tenantDisplayName, setupLink string) error {
	return SendNotification(ctx, buildCustomerPortalInviteNotification(tenantID, resourceID, recipientEmail, recipientName, tenantDisplayName, setupLink))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildCustomerPortalInviteNotification -v`
Expected: PASS

- [ ] **Step 5: Update call site**

In `controllers/customer_portal_admin.go`, add `"strconv"` to the import block (`controllers/customer_portal_admin.go:4-17`):

```go
import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/authz"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)
```

Then at `controllers/customer_portal_admin.go:122`, change:
```go
	if err := services.SendCustomerPortalInviteEmail(req.Email, req.FullName, tenant.DisplayName, link); err != nil {
```
to:
```go
	if err := services.SendCustomerPortalInviteEmail(r.Context(), tenant.ID, strconv.Itoa(custInternalID), req.Email, req.FullName, tenant.DisplayName, link); err != nil {
```

(`custInternalID` is declared as `int` at `controllers/customer_portal_admin.go:69` — `NotificationRequest.ResourceID` is a `string`, hence `strconv.Itoa`.)

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/customer_portal_admin.go
git commit -m "feat(notify): route customer portal invite email through Notify"
```

---

## Task 7: Migrate `SendCustomerNoteConfirmationEmail`

**Files:**
- Modify: `services/email.go`
- Modify: `controllers/customer_portal.go:34-36`
- Test: `services/email_test.go`

**Interfaces:**
- Produces: `services.SendCustomerNoteConfirmationEmail(ctx context.Context, tenantID, noteID, recipientEmail, recipientName, tenantDisplayName string) error`.

- [ ] **Step 1: Write the failing test**

Append to `services/email_test.go`:

```go
func TestBuildCustomerNoteConfirmationNotification(t *testing.T) {
	req := buildCustomerNoteConfirmationNotification("tenant-1", "note-1", "buyer@example.com", "Casey Buyer", "Acme Stone Co")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Equal(t, "customer_note.confirmed", req.EventType)
	assert.Equal(t, "customer_note", req.Resource)
	assert.Equal(t, "note-1", req.ResourceID)
	assert.Equal(t, "Your note to Acme Stone Co was sent", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Equal(t, []string{"email"}, req.Channels)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/... -run TestBuildCustomerNoteConfirmationNotification -v`
Expected: FAIL with `undefined: buildCustomerNoteConfirmationNotification`

- [ ] **Step 3: Implement**

In `services/email.go`, replace `SendCustomerNoteConfirmationEmail`:

```go
// buildCustomerNoteConfirmationNotification builds the Notify request
// confirming a portal-submitted note was received.
func buildCustomerNoteConfirmationNotification(tenantID, noteID, recipientEmail, recipientName, tenantDisplayName string) NotificationRequest {
	subject := "Your note to " + tenantDisplayName + " was sent"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Your note was sent successfully</h2>
			<p>Hello%s,</p>
			<p>Your note has been delivered to <strong>%s</strong>. Their team will follow up with you as needed.</p>
			<p>Best regards,<br>%s</p>
		</body>
		</html>
	`, nameClause(recipientName), tenantDisplayName, tenantDisplayName)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		EventType:     "customer_note.confirmed",
		Resource:      "customer_note",
		ResourceID:    noteID,
		Title:         subject,
		Body:          "Note confirmation email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendCustomerNoteConfirmationEmail confirms to a customer that a note they
// submitted through the portal was received.
func SendCustomerNoteConfirmationEmail(ctx context.Context, tenantID, noteID, recipientEmail, recipientName, tenantDisplayName string) error {
	return SendNotification(ctx, buildCustomerNoteConfirmationNotification(tenantID, noteID, recipientEmail, recipientName, tenantDisplayName))
}
```

Also delete the now-unused `nameClause` guard only if no caller remains — it is still used by `buildUserInviteNotification`, `buildPasswordResetNotification`, `buildPortalInviteNotification`, and `buildCustomerPortalInviteNotification`, so leave it in place.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/... -run TestBuildCustomerNoteConfirmationNotification -v`
Expected: PASS

- [ ] **Step 5: Update call site**

In `controllers/customer_portal.go:34-36`, change:
```go
	if tenant, tErr := tenancy.TenantFromContext(r.Context()); tErr == nil {
		if mErr := services.SendCustomerNoteConfirmationEmail(note.Submitter.Email, note.Submitter.Name, tenant.DisplayName); mErr != nil {
```
to:
```go
	if tenant, tErr := tenancy.TenantFromContext(r.Context()); tErr == nil {
		if mErr := services.SendCustomerNoteConfirmationEmail(r.Context(), tenant.ID, note.ID, note.Submitter.Email, note.Submitter.Name, tenant.DisplayName); mErr != nil {
```

- [ ] **Step 6: Build to confirm the whole repo still compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go services/email_test.go controllers/customer_portal.go
git commit -m "feat(notify): route customer note confirmation email through Notify"
```

---

## Task 8: Delete dead Resend/SMTP code and verify

**Files:**
- Modify: `services/email.go` (delete `sendEmail`, `sendViaResend`, `sendViaSMTP`)

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing new — this task only removes code no longer called by any of the 7 functions migrated in Tasks 1–7.

- [ ] **Step 1: Confirm nothing still calls the functions being deleted**

Run: `grep -rn "sendEmail(\|sendViaResend(\|sendViaSMTP(" --include=*.go . | grep -v _test.go`
Expected: no output (Tasks 1–7 already replaced every caller)

- [ ] **Step 2: Delete `sendEmail`, `sendViaResend`, `sendViaSMTP`**

In `services/email.go`, delete these three functions and their doc comments in full:

```go
// sendEmail routes through the first available provider:
//  1. Resend API  — when RESEND_API_KEY is set
//  2. SMTP        — when SMTP_HOST + SENDER_EMAIL are set
//  3. No-op       — logs that no provider is configured, returns nil (non-fatal)
func sendEmail(to, subject, body string) error {
	...
}

// sendViaResend delivers the email through the Resend HTTP API.
// Docs: https://resend.com/docs/api-reference/emails/send-email
func sendViaResend(apiKey, from, to, subject, html string) error {
	...
}

// sendViaSMTP delivers the email through the configured SMTP server.
func sendViaSMTP(cfg config.Config, to, subject, body string) error {
	...
}
```

- [ ] **Step 3: Trim now-unused imports**

The file's only remaining functions (`nameClause`, the 7 `SendXEmail` wrappers, and their `build*Notification` helpers) use only `fmt.Sprintf` and the `context.Context` parameter type. Replace the import block with:

```go
package services

import (
	"context"
	"fmt"
)
```

- [ ] **Step 4: Build to confirm no orphaned imports or references**

Run: `go build ./...`
Expected: no errors (a leftover unused import or reference to a deleted function/type like `config.Config` would fail the build here)

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: PASS (all packages, including the 7 new `TestBuild*Notification` tests and the existing `services/notify_test.go`, `controllers/documents_test.go` suites)

- [ ] **Step 6: Run go vet**

Run: `go vet ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add services/email.go
git commit -m "chore(notify): delete dead Resend/SMTP transport now unused by services/email.go"
```

---

## Task 9: Push branch and open PR

**Files:** none (git/GitHub operations only)

- [ ] **Step 1: Push the branch**

Run: `git push -u origin feat/migrate-transactional-emails-to-notify`

- [ ] **Step 2: Open a PR against `develop`**

Run:
```bash
gh pr create --base develop --title "feat(notify): migrate remaining transactional emails to Notify" --body "$(cat <<'EOF'
## Summary
- Routes the 7 remaining direct Resend/SMTP transactional emails (onboarding invite, password setup, user invite, password reset, portal invite, customer portal invite, customer note confirmation) through stonesuite-notify's queue/retry/audit path, matching the document-send migration.
- All 7 use email-only recipients (no tenant-scoped users.id available at any of these call sites).
- Deletes the now-dead sendEmail/sendViaResend/sendViaSMTP Resend/SMTP transport.

## Design
See docs/superpowers/specs/2026-08-28-migrate-transactional-emails-to-notify-design.md

## Test plan
- [x] go build ./...
- [x] go test ./...
- [x] go vet ./...
- [ ] Manual smoke test against a real stonesuite-notify instance for at least one flow (e.g. forgot-password) before merge

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** D1 (no Notify changes) — no task touches `stonesuite-notify`. D2 (ctx+tenantID params) — Tasks 1–7. D3 (email-only recipients) — every builder test asserts `Recipients[0].Email` set, `UserID` empty. D4 (unchanged content) — every builder reuses the original HTML string verbatim, verified by `assert.Contains` on template-specific fragments. D5 (failure behavior unchanged) — call sites keep their existing `if err != nil { log.Printf(...) }` shape, only arguments change. D6 (delete dead code) — Task 8. D7 (leave config as-is) — no task touches `config/config.go`.
- **Placeholder scan:** none — every step has literal file paths, exact code, exact commands with expected output.
- **Type consistency:** `NotificationRequest`/`RecipientTarget` field names (`TenantID`, `Recipients`, `EventType`, `Resource`, `ResourceID`, `Title`, `Body`, `EmailBodyHTML`, `Channels`) used identically across all 7 builders, matching the existing struct in `services/notify.go` — no renames introduced.

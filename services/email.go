package services

import (
	"context"
	"fmt"
	"html"
)

// greeting renders "Hello {name}," (or "Hello,") with the name HTML-escaped —
// recipient/workspace names are caller-supplied and must not be trusted as
// markup.
func greeting(name string) string {
	return emailParagraph("Hello" + html.EscapeString(nameClause(name)) + ",")
}

// buildOnboardingInviteNotification builds the Notify request for a tenant
// onboarding invite email.
func buildOnboardingInviteNotification(tenantID, inviteID, recipientEmail, recipientName, inviteLink string) NotificationRequest {
	subject := "Your StoneSuite Onboarding Invitation"
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("You've been invited to complete an onboarding experience with StoneSuite. Use the button below to begin.")) +
		emailCTAAccent(inviteLink, "Start onboarding") +
		emailFinePrint("This invitation link is time-limited for security.")
	body := WrapEmailHTMLWithBanner("Complete your StoneSuite onboarding.", "Onboarding Invite", "You're invited to join", "StoneSuite", inner)
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

// buildPasswordSetupNotification builds the Notify request for a
// post-approval "set your password" email.
func buildPasswordSetupNotification(tenantID, identityID, recipientEmail, recipientName, setupLink string) NotificationRequest {
	subject := "Set up your StoneSuite account"
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("Your onboarding has been approved and your workspace is being set up. Set your password to finish activating your account.")) +
		emailCTAAccent(setupLink, "Set your password") +
		emailFinePrint("This link is time-limited for security.")
	body := WrapEmailHTMLWithBanner("Set your password to activate your StoneSuite account.", "Account Setup", "Your workspace", "is ready to go.", inner)
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

// buildUserInviteNotification builds the Notify request for a colleague
// workspace invite email.
func buildUserInviteNotification(tenantID, inviteID, actorUserID, recipientEmail, recipientName, workspaceName, inviteLink string) NotificationRequest {
	subject := "You've been invited to " + workspaceName
	ws := html.EscapeString(workspaceName)
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("A colleague has invited you to join the <strong>"+ws+"</strong> workspace on StoneSuite. Use the button below to accept and set your password.")) +
		emailCTAAccent(inviteLink, "Accept invitation") +
		emailFinePrint("This invitation expires in 48 hours. If you weren't expecting it, you can ignore this email.")
	body := WrapEmailHTMLWithBanner("You've been invited to join "+workspaceName+" on StoneSuite.", "Workspace Invite", "You're invited to join", workspaceName, inner)
	return NotificationRequest{
		TenantID:      tenantID,
		Recipients:    []RecipientTarget{{Email: recipientEmail}},
		ActorUserID:   actorUserID,
		EventType:     "user.invited",
		Resource:      "user",
		ResourceID:    inviteID,
		Title:         subject,
		Body:          "User invite email sent.",
		EmailBodyHTML: body,
		Channels:      []string{"email"},
	}
}

// SendUserInviteEmailWithResult sends the colleague workspace-invite email and
// returns the notify notification ids for later delivery reconciliation. An
// unparseable response body is not an error (see SendNotificationWithResult) —
// the ids just come back empty.
func SendUserInviteEmailWithResult(ctx context.Context, tenantID, inviteID, actorUserID, recipientEmail, recipientName, workspaceName, inviteLink string) (NotificationResult, error) {
	return SendNotificationWithResult(ctx, buildUserInviteNotification(
		tenantID, inviteID, actorUserID, recipientEmail, recipientName, workspaceName, inviteLink))
}

// SendUserInviteEmail sends an email to a colleague invited to join a tenant
// workspace. Thin wrapper over SendUserInviteEmailWithResult for callers that do
// not need the notification ids.
func SendUserInviteEmail(ctx context.Context, tenantID, inviteID, actorUserID, recipientEmail, recipientName, workspaceName, inviteLink string) error {
	_, err := SendUserInviteEmailWithResult(ctx, tenantID, inviteID, actorUserID, recipientEmail, recipientName, workspaceName, inviteLink)
	return err
}

// buildPasswordResetNotification builds the Notify request for a
// forgot-password reset-link email.
func buildPasswordResetNotification(tenantID, identityID, recipientEmail, recipientName, resetLink string) NotificationRequest {
	subject := "Reset your StoneSuite password"
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("We received a request to reset the password for your StoneSuite account. Use the button below to choose a new one — the link expires in 1 hour.")) +
		emailCTAAccent(resetLink, "Reset password") +
		emailFinePrint("If you didn't request a password reset, ignore this email — your password won't change.")
	body := WrapEmailHTMLWithBanner("Reset your StoneSuite password.", "Password Reset", "Reset your", "password.", inner)
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

// SendPasswordResetEmail sends a password-reset link to an existing account holder.
func SendPasswordResetEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, resetLink string) error {
	return SendNotification(ctx, buildPasswordResetNotification(tenantID, identityID, recipientEmail, recipientName, resetLink))
}

// buildPortalInviteNotification builds the Notify request for an approved
// customer's portal-login setup invite.
func buildPortalInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) NotificationRequest {
	subject := workspaceName + " — set up your customer portal access"
	ws := html.EscapeString(workspaceName)
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("<strong>"+ws+"</strong> has given you access to their customer portal, where you can view your sales orders, invoices, payments and refunds at any time.")) +
		emailCTAAccent(setupLink, "Set up access") +
		emailFinePrint(fmt.Sprintf("This link expires in %d hours. If you weren't expecting this email, you can ignore it.", expiryHours))
	body := WrapEmailHTMLWithBanner(workspaceName+" gave you customer portal access.", "Portal Access", "Your customer", "portal is ready.", inner)
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

// buildCustomerPortalInviteNotification builds the Notify request for an
// external customer's portal-login setup invite.
func buildCustomerPortalInviteNotification(tenantID, resourceID, recipientEmail, recipientName, tenantDisplayName, setupLink string) NotificationRequest {
	subject := "You've been invited to the " + tenantDisplayName + " customer portal"
	td := html.EscapeString(tenantDisplayName)
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("<strong>"+td+"</strong> has invited you to their customer portal, where you can submit notes and questions directly to their team.")) +
		emailCTAAccent(setupLink, "Set your password") +
		emailFinePrint("This invitation link is time-limited for security.")
	body := WrapEmailHTMLWithBanner(tenantDisplayName+" invited you to their customer portal.", "Portal Invite", "You're invited to the", "customer portal.", inner)
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

// buildCustomerNoteConfirmationNotification builds the Notify request
// confirming a portal-submitted note was received.
func buildCustomerNoteConfirmationNotification(tenantID, noteID, recipientEmail, recipientName, tenantDisplayName string) NotificationRequest {
	subject := "Your note to " + tenantDisplayName + " was sent"
	td := html.EscapeString(tenantDisplayName)
	inner := greeting(recipientName) +
		EmailMessageBox(emailParagraph("Your note has been delivered to <strong>"+td+"</strong>. Their team will follow up with you as needed."))
	body := WrapEmailHTMLWithBanner("Your note to "+tenantDisplayName+" was delivered.", "Receipt Confirmed", "Your note was", "delivered.", inner)
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

// nameClause formats " {name}" with a leading space, or "" when name is blank.
func nameClause(name string) string {
	if name == "" {
		return ""
	}
	return " " + name
}

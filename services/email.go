package services

import (
	"context"
	"fmt"
)

// Every builder below supplies only the dynamic content of its email (an
// Email value); SendNotification renders it through the one shared template.

// buildOnboardingInviteNotification builds the Notify request for a tenant
// onboarding invite email.
func buildOnboardingInviteNotification(tenantID, inviteID, recipientEmail, recipientName, inviteLink string) NotificationRequest {
	return NotificationRequest{
		TenantID:   tenantID,
		Recipients: []RecipientTarget{{Email: recipientEmail}},
		EventType:  "tenant.onboarding_invited",
		Resource:   "tenant",
		ResourceID: inviteID,
		Title:      "Your StoneSuite Onboarding Invitation",
		Body:       "Onboarding invite email sent.",
		Channels:   []string{"email"},
		Email: &Email{
			Preheader: "Complete your StoneSuite onboarding.", Badge: "Onboarding Invite", Icon: IconEnvelope,
			Heading: "You're invited to join", HeadingAccent: "StoneSuite",
			Subtitle: "Let's build something great together.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Text("You've been invited to set up your company on StoneSuite. Use the button below to fill in your company details.")},
				{Text("Your application will be reviewed, and once it's approved you'll receive another email with a link to set your password and open your workspace.")},
			},
			ActionURL: inviteLink, ActionLabel: "Start onboarding",
			FinePrint: []string{"This invitation link is time-limited for security."},
			Reason:    "you've been invited to StoneSuite.",
		},
	}
}

// SendOnboardingInviteEmail sends an invitation email for customer onboarding.
func SendOnboardingInviteEmail(ctx context.Context, tenantID, inviteID, recipientEmail, recipientName, inviteLink string) error {
	return SendNotification(ctx, buildOnboardingInviteNotification(tenantID, inviteID, recipientEmail, recipientName, inviteLink))
}

// buildPasswordSetupNotification builds the Notify request for a
// post-approval "set your password" email.
func buildPasswordSetupNotification(tenantID, identityID, recipientEmail, recipientName, setupLink string) NotificationRequest {
	return NotificationRequest{
		TenantID:   tenantID,
		Recipients: []RecipientTarget{{Email: recipientEmail}},
		EventType:  "identity.password_setup",
		Resource:   "identity",
		ResourceID: identityID,
		Title:      "Set up your StoneSuite account",
		Body:       "Password setup email sent.",
		Channels:   []string{"email"},
		Email: &Email{
			Preheader: "Set your password to activate your StoneSuite account.", Badge: "Account Setup", Icon: IconWorkspace,
			Heading: "Your workspace", HeadingAccent: "is ready to go.",
			Subtitle: "Set your password to activate your account.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Text("Good news — your onboarding application has been approved and your workspace is ready. Set your password to activate your account and sign in for the first time.")},
			},
			ActionURL: setupLink, ActionLabel: "Set your password",
			FinePrint: []string{"This link is time-limited for security."},
		},
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
	return NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []RecipientTarget{{Email: recipientEmail}},
		ActorUserID: actorUserID,
		EventType:   "user.invited",
		Resource:    "user",
		ResourceID:  inviteID,
		Title:       "You've been invited to " + workspaceName,
		Body:        "User invite email sent.",
		Channels:    []string{"email"},
		Email: &Email{
			Preheader: "You've been invited to join " + workspaceName + " on StoneSuite.", Badge: "Workspace Invite", Icon: IconTeam,
			Heading: "You're invited to join", HeadingAccent: workspaceName,
			Subtitle: "Collaborate. Manage. Grow together.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Text("You've been invited to join the "), Bold(workspaceName), Text(" workspace on StoneSuite. Accept the invitation to set your password and sign in to your team's workspace.")},
			},
			ActionURL: inviteLink, ActionLabel: "Accept invitation",
			FinePrint: []string{"This invitation expires in 48 hours."},
			Reason:    "you've been invited to a workspace.",
		},
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
	return NotificationRequest{
		TenantID:   tenantID,
		Recipients: []RecipientTarget{{Email: recipientEmail}},
		EventType:  "identity.password_reset",
		Resource:   "identity",
		ResourceID: identityID,
		Title:      "Reset your StoneSuite password",
		Body:       "Password reset email sent.",
		Channels:   []string{"email"},
		Email: &Email{
			Preheader: "Reset your StoneSuite password.", Badge: "Password Reset", Icon: IconLock,
			Heading: "Reset your", HeadingAccent: "password.",
			Subtitle: "Keep your account secure.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Text("We received a request to reset the password for your StoneSuite account. Use the button below to choose a new one.")},
			},
			ActionURL: resetLink, ActionLabel: "Reset password",
			FinePrint: []string{
				"This link expires in 1 hour.",
				"If you didn't request a password reset, please ignore this email — your password will remain unchanged.",
			},
		},
	}
}

// SendPasswordResetEmail sends a password-reset link to an existing account holder.
func SendPasswordResetEmail(ctx context.Context, tenantID, identityID, recipientEmail, recipientName, resetLink string) error {
	return SendNotification(ctx, buildPasswordResetNotification(tenantID, identityID, recipientEmail, recipientName, resetLink))
}

// buildPortalInviteNotification builds the Notify request for an approved
// customer's portal-login setup invite.
func buildPortalInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) NotificationRequest {
	return NotificationRequest{
		TenantID:   tenantID,
		Recipients: []RecipientTarget{{Email: recipientEmail}},
		EventType:  "portal_user.invited",
		Resource:   "portal_user",
		ResourceID: inviteID,
		Title:      workspaceName + " — set up your customer portal access",
		Body:       "Portal invite email sent.",
		Channels:   []string{"email"},
		Email: &Email{
			Preheader: workspaceName + " gave you customer portal access.", Badge: "Portal Access", Icon: IconPortal,
			Heading: "Your customer", HeadingAccent: "portal is ready.",
			Subtitle: "Access your information anytime.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Bold(workspaceName), Text(" has given you access to their customer portal, where you can view your sales orders, invoices, payments and refunds at any time. Set up your access to create your password and sign in.")},
			},
			ActionURL: setupLink, ActionLabel: "Set up access",
			FinePrint: []string{fmt.Sprintf("This link expires in %d hours.", expiryHours)},
			Reason:    "you've been given portal access.",
		},
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
	return NotificationRequest{
		TenantID:   tenantID,
		Recipients: []RecipientTarget{{Email: recipientEmail}},
		EventType:  "customer_portal.invited",
		Resource:   "customer_portal",
		ResourceID: resourceID,
		Title:      "You've been invited to the " + tenantDisplayName + " customer portal",
		Body:       "Customer portal invite email sent.",
		Channels:   []string{"email"},
		Email: &Email{
			Preheader: tenantDisplayName + " invited you to their customer portal.", Badge: "Portal Invite", Icon: IconDocPlus,
			Heading: "You're invited to the", HeadingAccent: "customer portal.",
			Subtitle: "View documents, submit notes and stay connected.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Bold(tenantDisplayName), Text(" has invited you to their customer portal, where you can review your documents and send notes and questions directly to their team. Set your password to sign in for the first time.")},
			},
			ActionURL: setupLink, ActionLabel: "Set your password",
			FinePrint: []string{"This invitation link is time-limited for security."},
			Reason:    "you've been invited to a portal.",
		},
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
	return NotificationRequest{
		TenantID:   tenantID,
		Recipients: []RecipientTarget{{Email: recipientEmail}},
		EventType:  "customer_note.confirmed",
		Resource:   "customer_note",
		ResourceID: noteID,
		Title:      "Your note to " + tenantDisplayName + " was sent",
		Body:       "Note confirmation email sent.",
		Channels:   []string{"email"},
		Email: &Email{
			Preheader: "Your note to " + tenantDisplayName + " was delivered.", Badge: "Receipt Confirmed", Icon: IconDocCheck,
			Heading: "Your note was", HeadingAccent: "delivered.",
			Subtitle: "We'll follow up as needed.",
			Greet:    true, RecipientName: recipientName,
			Paragraphs: []Paragraph{
				{Text("Your note has been delivered to "), Bold(tenantDisplayName), Text(". Their team will review it and follow up with you if anything more is needed.")},
			},
			ActionURL: PortalURL(), ActionLabel: "View your notes",
		},
	}
}

// SendCustomerNoteConfirmationEmail confirms to a customer that a note they
// submitted through the portal was received.
func SendCustomerNoteConfirmationEmail(ctx context.Context, tenantID, noteID, recipientEmail, recipientName, tenantDisplayName string) error {
	return SendNotification(ctx, buildCustomerNoteConfirmationNotification(tenantID, noteID, recipientEmail, recipientName, tenantDisplayName))
}

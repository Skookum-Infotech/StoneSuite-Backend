package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"

	"stonesuite-backend/config"
)

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

// buildPortalInviteNotification builds the Notify request for an approved
// customer's portal-login setup invite.
func buildPortalInviteNotification(tenantID, inviteID, recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) NotificationRequest {
	subject := workspaceName + " \u2014 set up your customer portal access"
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

// SendCustomerPortalInviteEmail invites an external customer to set a
// password and activate their customer-portal login.
func SendCustomerPortalInviteEmail(recipientEmail, recipientName, tenantDisplayName, setupLink string) error {
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
	return sendEmail(recipientEmail, subject, body)
}

// SendCustomerNoteConfirmationEmail confirms to a customer that a note they
// submitted through the portal was received.
func SendCustomerNoteConfirmationEmail(recipientEmail, recipientName, tenantDisplayName string) error {
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
	return sendEmail(recipientEmail, subject, body)
}

// nameClause formats " {name}" with a leading space, or "" when name is blank.
func nameClause(name string) string {
	if name == "" {
		return ""
	}
	return " " + name
}

// sendEmail routes through the first available provider:
//  1. Resend API  — when RESEND_API_KEY is set
//  2. SMTP        — when SMTP_HOST + SENDER_EMAIL are set
//  3. No-op       — logs that no provider is configured, returns nil (non-fatal)
func sendEmail(to, subject, body string) error {
	cfg := config.AppConfig

	if cfg.ResendAPIKey != "" {
		return sendViaResend(cfg.ResendAPIKey, cfg.SenderEmail, to, subject, body)
	}
	if cfg.SMTPHost != "" && cfg.SenderEmail != "" {
		return sendViaSMTP(cfg, to, subject, body)
	}

	log.Printf("INFO: no email provider configured (set RESEND_API_KEY or SMTP_HOST+SENDER_EMAIL) — skipping email to %s", to)
	return nil
}

// sendViaResend delivers the email through the Resend HTTP API.
// Docs: https://resend.com/docs/api-reference/emails/send-email
func sendViaResend(apiKey, from, to, subject, html string) error {
	if from == "" {
		from = "noreply@stonesuite.app"
	}
	payload, err := json.Marshal(map[string]any{
		"from":    from,
		"to":      []string{to},
		"subject": subject,
		"html":    html,
	})
	if err != nil {
		return fmt.Errorf("resend: marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("resend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("resend: send to %s: %w", to, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("ERROR: Resend API to %s returned HTTP %d: %s", to, resp.StatusCode, respBody)
		return fmt.Errorf("resend: HTTP %d for %s: %s", resp.StatusCode, to, respBody)
	}

	log.Printf("Email sent via Resend to %s", to)
	return nil
}

// sendViaSMTP delivers the email through the configured SMTP server.
func sendViaSMTP(cfg config.Config, to, subject, body string) error {
	auth := smtp.PlainAuth("", cfg.SenderEmail, cfg.SenderPassword, cfg.SMTPHost)
	toList := strings.Split(to, ",")

	headers := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-version: 1.0;\r\nContent-Type: text/html; charset=\"UTF-8\";\r\n",
		cfg.SenderEmail, to, subject,
	)
	message := []byte(headers + "\r\n" + body)

	addr := fmt.Sprintf("%s:%s", cfg.SMTPHost, cfg.SMTPPort)
	if err := smtp.SendMail(addr, auth, cfg.SenderEmail, toList, message); err != nil {
		log.Printf("ERROR: smtp.SendMail to %s via %s failed: %v", to, addr, err)
		return fmt.Errorf("send email to %s: %w", to, err)
	}

	log.Printf("Email sent via SMTP to %s", to)
	return nil
}

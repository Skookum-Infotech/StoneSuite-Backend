package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"

	"stonesuite-backend/config"
)

// SendOnboardingInviteEmail sends an invitation email for customer onboarding.
func SendOnboardingInviteEmail(recipientEmail, recipientName, inviteLink string) error {
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
	return sendEmail(recipientEmail, subject, body)
}

// SendPasswordSetupEmail sends the "set your password" email after a customer's
// onboarding application is approved (or they are onboarded directly).
func SendPasswordSetupEmail(recipientEmail, recipientName, setupLink string) error {
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
	return sendEmail(recipientEmail, subject, body)
}

// SendUserInviteEmail sends an email to a colleague invited to join a tenant workspace.
func SendUserInviteEmail(recipientEmail, recipientName, workspaceName, inviteLink string) error {
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
	return sendEmail(recipientEmail, subject, body)
}

// SendPasswordResetEmail sends a password-reset link to an existing account holder.
func SendPasswordResetEmail(recipientEmail, recipientName, resetLink string) error {
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
	defer resp.Body.Close()
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

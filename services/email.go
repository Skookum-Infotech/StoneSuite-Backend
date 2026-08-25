package services

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/smtp"
	"net/textproto"
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
// SendPortalInviteEmail invites an approved customer to set up their portal
// login. Distinct from SendUserInviteEmail: the recipient is a customer, not a
// colleague joining the workspace, so the copy must not imply staff access.
func SendPortalInviteEmail(recipientEmail, recipientName, workspaceName, setupLink string, expiryHours int) error {
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
	return sendEmail(recipientEmail, subject, body)
}

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

// EmailAttachment is a single file attached to a document email.
type EmailAttachment struct {
	FileName    string
	ContentType string
	Content     []byte
}

// SendDocumentEmail sends an HTML email with optional file attachments to one
// or more recipients (plus optional CC), routing through the same provider
// precedence as sendEmail: Resend, then SMTP, then no-op.
func SendDocumentEmail(to, cc []string, subject, htmlBody string, attachments []EmailAttachment) error {
	cfg := config.AppConfig
	from := cfg.SenderEmail
	if from == "" {
		from = "noreply@stonesuite.app"
	}
	if len(to) == 0 {
		return fmt.Errorf("send document email: at least one recipient is required")
	}

	if cfg.ResendAPIKey != "" {
		return sendDocViaResend(cfg.ResendAPIKey, from, to, cc, subject, htmlBody, attachments)
	}
	if cfg.SMTPHost != "" && cfg.SenderEmail != "" {
		msg := buildMIME(from, to, cc, subject, htmlBody, attachments)
		auth := smtp.PlainAuth("", cfg.SenderEmail, cfg.SenderPassword, cfg.SMTPHost)
		addr := fmt.Sprintf("%s:%s", cfg.SMTPHost, cfg.SMTPPort)
		rcpt := append(append([]string{}, to...), cc...)
		if err := smtp.SendMail(addr, auth, from, rcpt, msg); err != nil {
			return fmt.Errorf("send document email via smtp: %w", err)
		}
		log.Printf("Document email sent via SMTP to %s", strings.Join(to, ","))
		return nil
	}

	log.Printf("INFO: no email provider configured — skipping document email to %s", strings.Join(to, ","))
	return nil
}

// buildMIME assembles a multipart/mixed message: an HTML part plus one
// base64-encoded part per attachment.
func buildMIME(from string, to, cc []string, subject, htmlBody string, attachments []EmailAttachment) []byte {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Headers.
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	if len(cc) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mw.Boundary())

	// HTML part.
	htmlHdr := textproto.MIMEHeader{}
	htmlHdr.Set("Content-Type", "text/html; charset=UTF-8")
	htmlHdr.Set("Content-Transfer-Encoding", "quoted-printable")
	if pw, err := mw.CreatePart(htmlHdr); err == nil {
		_ = writeQuotedPrintable(pw, htmlBody)
	}

	// Attachment parts.
	for _, a := range attachments {
		ah := textproto.MIMEHeader{}
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.Set("Content-Type", ct)
		ah.Set("Content-Transfer-Encoding", "base64")
		ah.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, a.FileName))
		if pw, err := mw.CreatePart(ah); err == nil {
			b64 := base64.StdEncoding.EncodeToString(a.Content)
			// wrap at 76 chars per RFC 2045
			for i := 0; i < len(b64); i += 76 {
				end := i + 76
				if end > len(b64) {
					end = len(b64)
				}
				_, _ = pw.Write([]byte(b64[i:end] + "\r\n"))
			}
		}
	}
	_ = mw.Close()
	return buf.Bytes()
}

// writeQuotedPrintable encodes s as quoted-printable per RFC 2045, matching
// the Content-Transfer-Encoding header declared on the HTML part.
func writeQuotedPrintable(w io.Writer, s string) error {
	qw := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qw, s); err != nil {
		_ = qw.Close()
		return fmt.Errorf("quoted-printable encode: %w", err)
	}
	if err := qw.Close(); err != nil {
		return fmt.Errorf("quoted-printable close: %w", err)
	}
	return nil
}

// sendDocViaResend delivers an HTML email plus attachments through Resend.
func sendDocViaResend(apiKey, from string, to, cc []string, subject, html string, attachments []EmailAttachment) error {
	atts := make([]map[string]string, 0, len(attachments))
	for _, a := range attachments {
		atts = append(atts, map[string]string{
			"filename": a.FileName,
			"content":  base64.StdEncoding.EncodeToString(a.Content),
		})
	}
	payload, err := json.Marshal(map[string]any{
		"from": from, "to": to, "cc": cc, "subject": subject, "html": html, "attachments": atts,
	})
	if err != nil {
		return fmt.Errorf("resend: marshal document payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("resend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("resend: send document to %v: %w", to, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("resend: HTTP %d: %s", resp.StatusCode, body)
	}
	log.Printf("Document email sent via Resend to %s", strings.Join(to, ","))
	return nil
}

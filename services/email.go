package services

import (
	"fmt"
	"log"
	"net/smtp"
	"strings"

	"stonesuite-backend/config"
)

// EmailService handles email sending
type EmailService struct {
	SMTPHost       string
	SMTPPort       string
	SenderEmail    string
	SenderPassword string
}

// InitEmailService creates a new email service instance
func InitEmailService() *EmailService {
	return &EmailService{
		SMTPHost:       config.AppConfig.SMTPHost,
		SMTPPort:       config.AppConfig.SMTPPort,
		SenderEmail:    config.AppConfig.SenderEmail,
		SenderPassword: config.AppConfig.SenderPassword,
	}
}

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

// nameClause formats " {name}" with a leading space, or "" when name is blank.
func nameClause(name string) string {
	if name == "" {
		return ""
	}
	return " " + name
}

// sendEmail is a helper function to send emails
func sendEmail(to, subject, body string) error {
	es := InitEmailService()

	// Check if SMTP credentials are configured
	if es.SMTPHost == "" || es.SenderEmail == "" {
		log.Printf("WARNING: Email service not configured. Skipping email to %s", to)
		return nil // Don't fail if email is not configured
	}

	// Create email message
	auth := smtp.PlainAuth("", es.SenderEmail, es.SenderPassword, es.SMTPHost)
	to_list := strings.Split(to, ",")

	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-version: 1.0;\r\nContent-Type: text/html; charset=\"UTF-8\";\r\n",
		es.SenderEmail, to, subject)
	message := []byte(headers + "\r\n" + body)

	// Send email
	addr := fmt.Sprintf("%s:%s", es.SMTPHost, es.SMTPPort)
	if err := smtp.SendMail(addr, auth, es.SenderEmail, to_list, message); err != nil {
		return fmt.Errorf("send email to %s: %w", to, err)
		// log.Printf("Failed to send email to %s: %v", to, err)
		// return nil // Log error but don't fail - allow auth to continue

	}

	log.Printf("Email sent successfully to %s", to)
	return nil
}

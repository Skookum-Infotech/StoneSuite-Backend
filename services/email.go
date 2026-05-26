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

// SendPasswordResetEmail sends a password reset email
func SendPasswordResetEmail(recipientEmail, resetLink string) error {
	// Build email
	subject := "Password Reset Request"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Password Reset Request</h2>
			<p>Hello,</p>
			<p>We received a request to reset your password. If you didn't make this request, you can ignore this email.</p>
			<p>To reset your password, click the link below:</p>
			<p><a href="%s" style="background-color: #007bff; color: white; padding: 10px 20px; text-decoration: none; border-radius: 5px;">Reset Password</a></p>
			<p>Or copy and paste this link in your browser:</p>
			<p>%s</p>
			<p>This link will expire in 1 hour.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, resetLink, resetLink)

	return sendEmail(recipientEmail, subject, body)
}

// SendVerificationEmail sends an email verification code
func SendVerificationEmail(recipientEmail, verificationCode string) error {
	// Build email
	subject := "Email Verification Code"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Email Verification</h2>
			<p>Hello,</p>
			<p>Thank you for registering with StoneSuite. Please verify your email address using the code below:</p>
			<h1 style="text-align: center; letter-spacing: 5px; color: #007bff;">%s</h1>
			<p>This code will expire in 24 hours.</p>
			<p>If you didn't register this email, please ignore this message.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, verificationCode)

	return sendEmail(recipientEmail, subject, body)
}

// SendWelcomeEmail sends a welcome email to new users
func SendWelcomeEmail(recipientEmail, userName string) error {
	// Build email
	subject := "Welcome to StoneSuite"
	body := fmt.Sprintf(`
		<html>
		<body style="font-family: Arial, sans-serif; color: #333;">
			<h2>Welcome to StoneSuite, %s!</h2>
			<p>Hello %s,</p>
			<p>Your account has been successfully created. You can now log in and start using StoneSuite.</p>
			<p>If you have any questions or need support, feel free to contact us.</p>
			<p>Best regards,<br>StoneSuite Team</p>
		</body>
		</html>
	`, userName, userName)

	return sendEmail(recipientEmail, subject, body)
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
		log.Printf("Failed to send email to %s: %v", to, err)
		return nil // Log error but don't fail - allow auth to continue
	}

	log.Printf("Email sent successfully to %s", to)
	return nil
}

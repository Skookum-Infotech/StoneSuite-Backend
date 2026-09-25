package controllers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
)

// emailPreviewDirEnv names the directory TestWriteEmailPreviews writes to; the
// test is skipped when it is unset, so normal runs never touch the disk.
const emailPreviewDirEnv = "EMAIL_PREVIEW_DIR"

// TestWriteEmailPreviews sends every customer/staff email through its real
// builder and services.SendNotification to a fake notify service, and saves
// the exact HTML that would be delivered as NN-name.html for visual review
// (see scripts/email-previews.sh, which also prints them to one PDF). The
// approval-requested email (10) is written by approvalchain's twin test.
//
//	EMAIL_PREVIEW_DIR=/tmp/emails go test ./controllers ./approvalchain -run TestWriteEmailPreviews -count=1
func TestWriteEmailPreviews(t *testing.T) {
	dir := os.Getenv(emailPreviewDirEnv)
	if dir == "" {
		t.Skip(emailPreviewDirEnv + " not set")
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ctx := context.Background()
	pdf := make([]byte, 245*1024)
	seller := docpdf.Seller{Name: "Acme Stone Co"}
	meta := DocMeta{WorkflowKey: "invoice", DefaultRecipientEmail: "pat@example.com", DefaultRecipientName: "Pat Customer"}

	previews := []struct {
		file string
		send func() error
	}{
		{"01-onboarding-invite", func() error {
			return services.SendOnboardingInviteEmail(ctx, "t", "i", "jane@example.com", "Jane Owner", "https://app.stonesuite.io/apply?token=x")
		}},
		{"02-account-setup", func() error {
			return services.SendPasswordSetupEmail(ctx, "t", "i", "sam@example.com", "Sam Customer", "https://app.stonesuite.io/set-password?token=x")
		}},
		{"03-workspace-invite", func() error {
			return services.SendUserInviteEmail(ctx, "t", "i", "a", "alex@example.com", "Alex Colleague", "Acme Stone Co", "https://app.stonesuite.io/accept?token=x")
		}},
		{"04-password-reset", func() error {
			return services.SendPasswordResetEmail(ctx, "t", "i", "sam@example.com", "Sam Customer", "https://app.stonesuite.io/reset?token=x")
		}},
		{"05-portal-access", func() error {
			return services.SendPortalInviteEmail(ctx, "t", "i", "pat@example.com", "Pat Customer", "Acme Stone Co", "https://app.stonesuite.io/portal/setup?token=x", 72)
		}},
		{"06-portal-invite", func() error {
			return services.SendCustomerPortalInviteEmail(ctx, "t", "i", "pat@example.com", "Pat Customer", "Acme Stone Co", "https://app.stonesuite.io/portal/accept-invite?token=x")
		}},
		{"07-note-received", func() error {
			return services.SendCustomerNoteConfirmationEmail(ctx, "t", "i", "pat@example.com", "Pat Customer", "Acme Stone Co")
		}},
		{"08-document-delivered", func() error {
			return services.SendNotification(ctx, customerSendRequest("t", "a", meta, "rec-1", "Invoice INV-0042",
				docpdf.PrintableDoc{Kind: "INVOICE", Number: "INV-0042", Seller: seller}, "", []string{"pat@example.com"}, nil, "INV-0042.pdf", pdf))
		}},
		{"09-customer-welcome", func() error {
			return services.SendNotification(ctx, customerWelcomeRequest("t", "Acme Stone Co", "rec-1", "pat@example.com", "Pat Customer"))
		}},
		{"11-customer-approved", func() error {
			return services.SendNotification(ctx, buildCustomerApprovedNotification("t", "a",
				services.RecipientTarget{UserID: "u", Email: "pat@example.com", Name: "Pat Customer"},
				customerApproval{Resource: "invoice", DisplayName: "Invoice", Number: "INV-000123", Amount: 12450, RecordUUID: "rec-1", ApprovedAt: time.Now()}))
		}},
		{"12-owner-document-sent", func() error {
			notifyOwnerOfSend(ctx, services.SendNotification, sendOwner{Email: "jane@example.com", IdentityID: "u", Name: "Jane Owner"}, "t", "a",
				docpdf.PrintableDoc{Kind: "INVOICE", Seller: seller}, "INV-000123", "invoice", "rec-1", []string{"pat@example.com"}, pdf, "INV-000123.pdf")
			return nil
		}},
	}
	for _, p := range previews {
		got := fakeNotify(t)
		config.AppConfig.FrontendURL = "https://app.stonesuite.io"
		config.AppConfig.EmailViewInBrowserURL = "https://app.stonesuite.io"
		config.AppConfig.SupportEmail = "hello@stonesuite.app"
		config.AppConfig.EmailPreferencesURL = "https://app.stonesuite.io/settings/notifications"
		config.AppConfig.EmailSocialLinkedInURL = "https://www.linkedin.com/company/stonesuite"
		config.AppConfig.EmailSocialXURL = "https://x.com/stonesuite"
		config.AppConfig.EmailSocialYouTubeURL = "https://www.youtube.com/@stonesuite"

		require.NoError(t, p.send(), p.file)
		require.Len(t, *got, 1, p.file)
		html, _ := (*got)[0]["emailBodyHtml"].(string)
		require.NotEmpty(t, html, p.file)
		require.NoError(t, os.WriteFile(filepath.Join(dir, p.file+".html"), []byte(html), 0o644))
	}
}

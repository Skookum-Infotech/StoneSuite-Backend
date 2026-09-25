package approvalchain

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

// TestWriteEmailPreviews writes the approval-requested email (10) for visual
// review; the other eleven come from controllers' twin test. Skipped unless
// EMAIL_PREVIEW_DIR is set.
func TestWriteEmailPreviews(t *testing.T) {
	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	if dir == "" {
		t.Skip("EMAIL_PREVIEW_DIR not set")
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	prev := config.AppConfig
	config.AppConfig = config.Config{
		FrontendURL: "https://app.stonesuite.io", EmailViewInBrowserURL: "https://app.stonesuite.io",
		EmailBrandName: "StoneSuite", SupportEmail: "hello@stonesuite.app",
		EmailPreferencesURL:    "https://app.stonesuite.io/settings/notifications",
		EmailSocialLinkedInURL: "https://www.linkedin.com/company/stonesuite", EmailSocialXURL: "https://x.com/stonesuite",
		EmailSocialYouTubeURL: "https://www.youtube.com/@stonesuite",
	}
	t.Cleanup(func() { config.AppConfig = prev })

	ec := EventContext{Resource: "invoice", DisplayName: "Invoice", RecordUUID: "rec-1"}
	facts := recordFacts{Number: "INV-000123", Amount: services.FormatEmailMoney("", 12450), OccurredAt: time.Now()}
	req := buildApprovalNotification("t", "invoice.approval_requested", "a", facts, ec, noteApprovalRequested,
		[]contact{{UserID: "u", Email: "alex@example.com", Name: "Alex Approver"}})
	e := *req.Email
	e.RecipientName = "Alex Approver" // what SendNotification fills in per recipient
	html, err := services.RenderEmail(e)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "10-approval-requested.html"), []byte(html), 0o644))
}

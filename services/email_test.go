package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
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
	assert.Equal(t, "Onboarding invite email sent.", req.Body)
	assert.Equal(t, "Your StoneSuite Onboarding Invitation", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Jane Owner")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/apply?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildPasswordSetupNotification(t *testing.T) {
	req := buildPasswordSetupNotification("tenant-1", "identity-1", "customer@example.com", "Sam Customer", "https://app.example/set-password?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "customer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "identity.password_setup", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Password setup email sent.", req.Body)
	assert.Equal(t, "Set up your StoneSuite account", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam Customer")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildUserInviteNotification(t *testing.T) {
	req := buildUserInviteNotification("tenant-1", "invite-1", "user-42", "colleague@example.com", "Alex Colleague", "Acme Stone Co", "https://app.example/accept?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "colleague@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "user-42", req.ActorUserID)
	assert.Equal(t, "user.invited", req.EventType)
	assert.Equal(t, "user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "User invite email sent.", req.Body)
	assert.Equal(t, "You've been invited to Acme Stone Co", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Alex Colleague")
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/accept?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildPasswordResetNotification(t *testing.T) {
	req := buildPasswordResetNotification("tenant-1", "identity-1", "user@example.com", "Sam User", "https://app.example/reset?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "user@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "identity.password_reset", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Password reset email sent.", req.Body)
	assert.Equal(t, "Reset your StoneSuite password", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam User")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/reset?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildPortalInviteNotification(t *testing.T) {
	req := buildPortalInviteNotification("tenant-1", "invite-1", "staffer@example.com", "Pat Staffer", "Acme Stone Co", "https://app.example/portal-setup?token=abc", 72)

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "staffer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "portal_user.invited", req.EventType)
	assert.Equal(t, "portal_user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "Portal invite email sent.", req.Body)
	assert.Equal(t, "Acme Stone Co — set up your customer portal access", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Pat Staffer")
	assert.Contains(t, req.EmailBodyHTML, "72 hours")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/portal-setup?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildCustomerPortalInviteNotification(t *testing.T) {
	req := buildCustomerPortalInviteNotification("tenant-1", "42", "buyer@example.com", "Casey Buyer", "Acme Stone Co", "https://portal.example/set-password?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "customer_portal.invited", req.EventType)
	assert.Equal(t, "customer_portal", req.Resource)
	assert.Equal(t, "42", req.ResourceID)
	assert.Equal(t, "Customer portal invite email sent.", req.Body)
	assert.Equal(t, "You've been invited to the Acme Stone Co customer portal", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Casey Buyer")
	assert.Contains(t, req.EmailBodyHTML, "https://portal.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildCustomerNoteConfirmationNotification(t *testing.T) {
	req := buildCustomerNoteConfirmationNotification("tenant-1", "note-1", "buyer@example.com", "Casey Buyer", "Acme Stone Co")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "customer_note.confirmed", req.EventType)
	assert.Equal(t, "customer_note", req.Resource)
	assert.Equal(t, "note-1", req.ResourceID)
	assert.Equal(t, "Note confirmation email sent.", req.Body)
	assert.Equal(t, "Your note to Acme Stone Co was sent", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Casey Buyer")
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Equal(t, []string{"email"}, req.Channels)
}

// TestTransactionalEmailBuilders_ContractHolds is a recurrence guard: every
// build*Notification helper must produce a request stonesuite-notify can
// actually deliver as an email. A builder that forgets Channels, EmailBodyHTML,
// or the recipient address ships an email that notify silently drops or
// misroutes -- exactly the class of silent failure this package keeps
// re-introducing. Add a row here whenever a new transactional email is added.
func TestTransactionalEmailBuilders_ContractHolds(t *testing.T) {
	builders := map[string]NotificationRequest{
		"onboarding_invite":      buildOnboardingInviteNotification("t", "id", "to@x.com", "Name", "https://x/apply?token=abc"),
		"password_setup":         buildPasswordSetupNotification("t", "id", "to@x.com", "Name", "https://x/set?token=abc"),
		"user_invite":            buildUserInviteNotification("t", "id", "actor", "to@x.com", "Name", "Workspace", "https://x/accept?token=abc"),
		"password_reset":         buildPasswordResetNotification("t", "id", "to@x.com", "Name", "https://x/reset?token=abc"),
		"portal_invite":          buildPortalInviteNotification("t", "id", "to@x.com", "Name", "Workspace", "https://x/portal?token=abc", 72),
		"customer_portal_invite": buildCustomerPortalInviteNotification("t", "id", "to@x.com", "Name", "Workspace", "https://x/portal?token=abc"),
		"customer_note_confirm":  buildCustomerNoteConfirmationNotification("t", "id", "to@x.com", "Name", "Workspace"),
	}

	for name, req := range builders {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, []string{"email"}, req.Channels, "must target the email channel")
			assert.NotEmpty(t, req.EventType, "EventType drives notify routing/audit")
			assert.NotEmpty(t, req.Resource, "Resource drives notify routing/audit")
			assert.NotEmpty(t, req.ResourceID, "ResourceID correlates the notification")
			assert.NotEmpty(t, req.Title, "Title is the email subject fallback")
			assert.NotEmpty(t, req.EmailBodyHTML, "an empty body ships a blank email")
			require.Len(t, req.Recipients, 1, "transactional emails go to exactly one address")
			assert.NotEmpty(t, req.Recipients[0].Email, "email channel needs a recipient address")
			assert.Empty(t, req.Recipients[0].UserID, "external recipients are addressed by email, not user id")
		})
	}
}

func TestSendUserInviteEmailWithResult_ParsesIDsAndSetsActor(t *testing.T) {
	var got NotificationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-1"}]}}`))
	}))
	defer server.Close()
	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendUserInviteEmailWithResult(context.Background(),
		"tenant-1", "invite-1", "user-42",
		"colleague@example.com", "Alex Colleague", "Acme Stone Co",
		"https://app.example/accept?token=abc")

	require.NoError(t, err)
	assert.Equal(t, []string{"n-1"}, res.NotificationIDs)
	assert.Equal(t, "user-42", got.ActorUserID)
	assert.Equal(t, "user.invited", got.EventType)
	require.Len(t, got.Recipients, 1)
	assert.Equal(t, "colleague@example.com", got.Recipients[0].Email)
	assert.Empty(t, got.Recipients[0].UserID)
}

func TestSendUserInviteEmailWithResult_UnparseableBodyIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated) // empty body: notify accepted, no ids echoed
	}))
	defer server.Close()
	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendUserInviteEmailWithResult(context.Background(),
		"tenant-1", "invite-1", "user-42",
		"colleague@example.com", "Alex Colleague", "Acme Stone Co",
		"https://app.example/accept?token=abc")

	require.NoError(t, err)
	assert.Empty(t, res.NotificationIDs)
}

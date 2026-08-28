package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, "identity.password_setup", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Set up your StoneSuite account", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam Customer")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

func TestBuildUserInviteNotification(t *testing.T) {
	req := buildUserInviteNotification("tenant-1", "invite-1", "colleague@example.com", "Alex Colleague", "Acme Stone Co", "https://app.example/accept?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "colleague@example.com", req.Recipients[0].Email)
	assert.Equal(t, "user.invited", req.EventType)
	assert.Equal(t, "user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "You've been invited to Acme Stone Co", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Alex Colleague")
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/accept?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
}

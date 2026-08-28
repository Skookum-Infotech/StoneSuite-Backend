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

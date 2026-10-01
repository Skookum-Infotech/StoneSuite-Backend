package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
)

// The alert for a bounced email is filed under an RBAC resource so the sender's
// bell shows it. These literals live in services (which does not import authz),
// so pin them to authz's names: a rename there must fail here, not silently hide
// every alert.
func TestStatusResourcesMatchRBAC(t *testing.T) {
	assert.Equal(t, string(authz.ResourceUser), statusResourceUser)
	assert.Equal(t, string(authz.ResourcePortalAccess), statusResourcePortalAccess)
	assert.Equal(t, string(authz.ResourceCustomer), statusResourceCustomer)
}

func TestInviteBuilders_ActorAndStatusLink(t *testing.T) {
	t.Run("staff invite links to the Users page", func(t *testing.T) {
		req := buildUserInviteNotification("t1", "inv-1", "identity-9", "a@b.com", "A", "Acme", "https://x/accept")
		assert.Equal(t, "identity-9", req.ActorUserID)
		assert.Equal(t, "/config/users", req.StatusLink)
		assert.Equal(t, "user", req.StatusResource)
	})

	t.Run("portal invite carries the granting staff member, a customer link and the portal_access resource", func(t *testing.T) {
		req := buildPortalInviteNotification("t1", "inv-1", "identity-9", "/crm/customer/c-1",
			"a@b.com", "A", "Acme", "https://x/setup", 72)
		assert.Equal(t, "identity-9", req.ActorUserID)
		assert.Equal(t, "/crm/customer/c-1", req.StatusLink)
		assert.Equal(t, "portal_access", req.StatusResource, "portal_user (the email's own resource) is not an RBAC resource")
		assert.Equal(t, "portal_user", req.Resource)
	})

	t.Run("customer portal invite carries the inviting staff member and the customer resource", func(t *testing.T) {
		req := buildCustomerPortalInviteNotification("t1", "7", "identity-9", "/crm/customer/c-1",
			"a@b.com", "A", "Acme", "https://x/set")
		assert.Equal(t, "identity-9", req.ActorUserID)
		assert.Equal(t, "/crm/customer/c-1", req.StatusLink)
		assert.Equal(t, "customer", req.StatusResource)
	})

	t.Run("platform onboarding invite deliberately has no actor", func(t *testing.T) {
		req := buildOnboardingInviteNotification("t1", "inv-1", "a@b.com", "A", "https://x/apply")
		assert.Empty(t, req.ActorUserID, "a platform admin is not a user inside the invited tenant")
		assert.Empty(t, req.StatusLink)
		assert.Empty(t, req.StatusResource)
	})
}

func TestNotificationRequest_StatusFieldsOnTheWire(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-1"}]}}`))
	}))
	defer srv.Close()

	orig := config.AppConfig
	config.AppConfig.NotifyURL = srv.URL
	config.AppConfig.NotifyAPIKey = "k"
	t.Cleanup(func() { config.AppConfig = orig })

	_, err := SendNotificationWithResult(context.Background(), NotificationRequest{
		TenantID: "t1", Recipients: []RecipientTarget{{Email: "a@b.com"}},
		ActorUserID: "identity-9", EventType: "x", Resource: "user", ResourceID: "r1", Title: "T",
		StatusLink: "/config/users", StatusResource: "user", Channels: []string{"email"},
	})

	require.NoError(t, err)
	assert.Equal(t, "/config/users", got["statusLink"])
	assert.Equal(t, "user", got["statusResource"])
	assert.Equal(t, "identity-9", got["actorUserId"])
}

func TestNotificationRequest_StatusFieldsOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(NotificationRequest{TenantID: "t1"})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "statusLink")
	assert.NotContains(t, string(raw), "statusResource")
}

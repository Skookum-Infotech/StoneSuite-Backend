//go:build dbtest

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

// TestTenantOps_Activate_RejectsNonMatchingAdminDomain_DB proves the platform
// activation endpoint surfaces the Task 2 domain gate as 403, not the
// generic 500 an unhandled error would produce, and that the platform-admin
// grant is genuinely never made.
func TestTenantOps_Activate_RejectsNonMatchingAdminDomain_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "skookuminfotech.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })

	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(ctx, "activate-test-"+suffix, "Activate Test", true)
	require.NoError(t, err)
	// tenants.is_platform_owner is a database-enforced singleton (partial
	// unique index idx_tenants_platform_owner): only one row may hold it at
	// a time across the whole shared control-plane test DB. Delete this row
	// once the test finishes so it doesn't collide with a platform-owner
	// tenant created by another test in this binary.
	t.Cleanup(func() {
		_, err := cp.Pool().Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
		require.NoError(t, err)
	})
	identity, err := cp.CreateIdentity(ctx, tenant.ID, "outsider-"+suffix+"@example.com", "", "Outsider", false)
	require.NoError(t, err)

	rawToken := "test-token-" + suffix
	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])
	require.NoError(t, cp.SetIdentitySetupTokenHash(ctx, identity.ID, tokenHash, time.Now().Add(15*time.Minute)))

	h := &TenantOps{CP: cp}
	req := httptest.NewRequest(http.MethodPost, "/api/platform/activate", jsonBody(t, map[string]any{
		"token": rawToken, "password": "a-very-long-password-123",
	}))
	rec := httptest.NewRecorder()
	h.Activate(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp["success"].(bool))

	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	require.NoError(t, err)
	assert.False(t, isAdmin, "identity must not have been granted platform admin")

	// The domain gate now runs before the one-shot setup token is consumed,
	// so a denied activation must leave the token intact and still resolvable.
	stillValid, err := cp.IdentityBySetupTokenHash(ctx, tokenHash)
	require.NoError(t, err, "setup token must survive a domain-gate denial")
	assert.Equal(t, identity.ID, stillValid.ID)
}

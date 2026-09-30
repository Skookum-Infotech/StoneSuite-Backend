//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// TestUserOps_UpdateMyProfile_DB covers PATCH /api/tenant/users/me. The point of
// the route is that a member with NO roles at all -- so no user:update -- can
// still rename themselves, while nothing but their own display name changes.
func TestUserOps_UpdateMyProfile_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	ops := NewUserOps(cp, tenancy.NewRouter(nil))
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))
	ctx := context.Background()

	// A role-less member, and a second one who must never be touched.
	identity, me := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	_, other := seedRBACTestIdentity(t, cp, pool, tenant.ID, "other-password")

	patch := func(t *testing.T, identityID string, body any) (*httptest.ResponseRecorder, models.APIResponse) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/tenant/users/me", jsonBody(t, body))
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: identityID, Email: identity.Email, TenantID: tenant.ID, UserID: me.ID}))
		rec := httptest.NewRecorder()
		resolver.Middleware(http.HandlerFunc(ops.UpdateMyProfile)).ServeHTTP(rec, req)

		var resp models.APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return rec, resp
	}

	t.Run("role-less member renames themselves", func(t *testing.T) {
		rec, resp := patch(t, identity.ID, map[string]any{"fullName": "  Grace Hopper "})

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.True(t, resp.Success)

		got, err := userstore.GetUserByID(ctx, pool, me.ID)
		require.NoError(t, err)
		assert.Equal(t, "Grace Hopper", got.FullName, "name is stored trimmed")
		assert.Equal(t, "active", got.Status, "status is untouched")
		assert.Equal(t, me.Email, got.Email, "email is untouched")
	})

	t.Run("body cannot smuggle a status change or another user's id", func(t *testing.T) {
		rec, _ := patch(t, identity.ID, map[string]any{
			"fullName": "Still Me", "status": "suspended", "id": other.ID, "email": "evil@example.com",
		})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

		got, err := userstore.GetUserByID(ctx, pool, me.ID)
		require.NoError(t, err)
		assert.Equal(t, "Still Me", got.FullName)
		assert.Equal(t, "active", got.Status)
		assert.Equal(t, me.Email, got.Email)

		untouched, err := userstore.GetUserByID(ctx, pool, other.ID)
		require.NoError(t, err)
		assert.Equal(t, other.FullName, untouched.FullName, "another member's row is never reachable")
	})

	t.Run("invalid names are rejected with 400", func(t *testing.T) {
		for _, name := range []string{"", "   "} {
			rec, resp := patch(t, identity.ID, map[string]any{"fullName": name})
			assert.Equal(t, http.StatusBadRequest, rec.Code, "name %q", name)
			assert.Equal(t, "Name is required.", resp.Message)
		}
		rec, resp := patch(t, identity.ID, map[string]any{"fullName": strings.Repeat("a", maxProfileNameLen+1)})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "Name is too long.", resp.Message)

		got, err := userstore.GetUserByID(ctx, pool, me.ID)
		require.NoError(t, err)
		assert.Equal(t, "Still Me", got.FullName, "a rejected edit leaves the stored name alone")
	})

	t.Run("suspended member is refused", func(t *testing.T) {
		_, err := userstore.UpdateUser(ctx, pool, me.ID, "", "suspended")
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = userstore.UpdateUser(ctx, pool, me.ID, "", "active") })

		rec, resp := patch(t, identity.ID, map[string]any{"fullName": "Should Not Save"})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, resp.Success)

		got, err := userstore.GetUserByID(ctx, pool, me.ID)
		require.NoError(t, err)
		assert.Equal(t, "Still Me", got.FullName)
	})

	t.Run("identity with no user row in this workspace gets 404", func(t *testing.T) {
		rec, _ := patch(t, "00000000-0000-0000-0000-000000000000", map[string]any{"fullName": "Ghost"})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("missing identity in the token gets 401", func(t *testing.T) {
		rec, _ := patch(t, "", map[string]any{"fullName": "Anon"})
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

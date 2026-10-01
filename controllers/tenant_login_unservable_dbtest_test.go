//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
)

const loginTestPassword = "correct-password"

type loginFixture struct {
	h        *TenantOps
	cp       *tenancy.ControlPlane
	tenant   *tenancy.Tenant
	identity *tenancy.Identity
	dsn      string
}

// newLoginFixture seeds a servable tenant with one active staff user, so each
// test can then put the tenant into the state under test.
func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, _ := seedRBACTestIdentity(t, cp, pool, tenant.ID, loginTestPassword)
	return &loginFixture{
		h:        &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)},
		cp:       cp,
		tenant:   tenant,
		identity: identity,
		dsn:      dsn,
	}
}

func (f *loginFixture) login(t *testing.T, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": email, "password": password,
	}))
	rec := httptest.NewRecorder()
	f.h.TenantLogin(rec, req)
	return rec
}

func (f *loginFixture) refresh(t *testing.T, refreshToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshToken})
	rec := httptest.NewRecorder()
	f.h.RefreshSession(rec, req)
	return rec
}

func (f *loginFixture) setStatus(t *testing.T, tenantID, status string) {
	t.Helper()
	require.NoError(t, f.cp.SetTenantStatus(context.Background(), tenantID, status))
}

// seedHomeIdentity creates an identity homed in tenantID with a known password
// and no users row (a platform admin or a portal customer).
func (f *loginFixture) seedHomeIdentity(t *testing.T, tenantID, label string) *tenancy.Identity {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(loginTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	email := fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano())
	identity, err := f.cp.CreateIdentity(context.Background(), tenantID, email, string(hash), label, true)
	require.NoError(t, err)
	return identity
}

// seedUnservableOwner creates the platform-owner tenant (never provisioned, so
// not servable) and removes it afterwards: is_platform_owner is a singleton.
func (f *loginFixture) seedUnservableOwner(t *testing.T) *tenancy.Tenant {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	owner, err := f.cp.CreateTenant(context.Background(), "login-owner-"+suffix, "Login Owner", true)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.cp.Pool().Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, owner.ID)
	})
	require.False(t, f.mustTenant(t, owner.ID).Servable(), "precondition: the owner tenant is not servable")
	return owner
}

func (f *loginFixture) mustTenant(t *testing.T, id string) *tenancy.Tenant {
	t.Helper()
	got, err := f.cp.TenantByID(context.Background(), id)
	require.NoError(t, err)
	return got
}

func withAdminEmailDomain(t *testing.T, domain string) {
	t.Helper()
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = domain
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })
}

type authResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Code     string `json:"code"`
	Token    string `json:"token"`
	Kind     string `json:"kind"`
	TenantID string `json:"tenantId"`
	User     struct {
		IsPlatformAdmin bool `json:"isPlatformAdmin"`
	} `json:"user"`
}

func decodeAuth(t *testing.T, rec *httptest.ResponseRecorder) authResponse {
	t.Helper()
	var resp authResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body=%s", rec.Body.String())
	return resp
}

func TestTenantLogin_SuspendedTenantIsRefused_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setStatus(t, f.tenant.ID, tenancy.StatusSuspended)

	rec := f.login(t, f.identity.Email, loginTestPassword)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.False(t, resp.Success)
	assert.Equal(t, models.CodeWorkspaceSuspended, resp.Code)
	assert.Contains(t, resp.Message, "suspended")
	assert.Empty(t, resp.Token, "no access token may be issued")
	assert.Empty(t, rec.Result().Cookies(), "no session cookies may be set")
}

func TestTenantLogin_DeletedTenantIsRefused_DB(t *testing.T) {
	f := newLoginFixture(t)
	require.NoError(t, f.cp.MarkTenantDeleted(context.Background(), f.tenant.ID, time.Now()))

	rec := f.login(t, f.identity.Email, loginTestPassword)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.Equal(t, models.CodeWorkspaceDeleted, resp.Code)
	assert.Empty(t, resp.Token)
	assert.Empty(t, rec.Result().Cookies())
}

// The workspace's state is only disclosed to someone who proved they own the
// account; a wrong password must not reveal that a tenant is suspended.
func TestTenantLogin_WrongPasswordNeverRevealsTenantState_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setStatus(t, f.tenant.ID, tenancy.StatusSuspended)

	rec := f.login(t, f.identity.Email, "wrong-password")

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.Equal(t, "Invalid email or password.", resp.Message)
	assert.Empty(t, resp.Code)
}

// An operator must always be able to reach the platform console, even when
// their home workspace is unhealthy (e.g. a failed migration).
func TestTenantLogin_PlatformAdminIsNotLockedOutByAnUnservableHomeTenant_DB(t *testing.T) {
	f := newLoginFixture(t)
	withAdminEmailDomain(t, "example.com")
	owner := f.seedUnservableOwner(t)
	admin := f.seedHomeIdentity(t, owner.ID, "platform-admin")
	require.NoError(t, f.cp.AddPlatformAdmin(context.Background(), admin.ID))

	rec := f.login(t, admin.Email, loginTestPassword)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.NotEmpty(t, resp.Token)
	assert.True(t, resp.User.IsPlatformAdmin)
}

// A portal customer whose *home* tenant is suspended may still hold active
// links to other workspaces; suspending one business must not lock them out of
// the others.
func TestTenantLogin_PortalCustomerWithSuspendedHomeTenantUsesAnotherWorkspace_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setStatus(t, f.tenant.ID, tenancy.StatusSuspended)
	other := seedServableCustomerTestTenant(t, f.cp, f.dsn)
	customer := f.seedHomeIdentity(t, f.tenant.ID, "portal-customer")
	_, err := f.cp.CreatePortalLink(context.Background(), customer.ID, other.ID)
	require.NoError(t, err)

	rec := f.login(t, customer.Email, loginTestPassword)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.Equal(t, middleware.KindPortal, resp.Kind)
	assert.Equal(t, other.ID, resp.TenantID, "the session lands in the workspace that is still active")
}

func TestTenantLogin_PortalCustomerWhoseOnlyWorkspaceIsSuspendedIsRefused_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setStatus(t, f.tenant.ID, tenancy.StatusSuspended)
	customer := f.seedHomeIdentity(t, f.tenant.ID, "portal-customer")
	_, err := f.cp.CreatePortalLink(context.Background(), customer.ID, f.tenant.ID)
	require.NoError(t, err)

	rec := f.login(t, customer.Email, loginTestPassword)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.Empty(t, resp.Token)
	assert.Contains(t, resp.Message, "portal access")
}

func TestRefreshSession_SuspendedTenantIsRefusedAndSessionEnded_DB(t *testing.T) {
	f := newLoginFixture(t)
	loginRec := f.login(t, f.identity.Email, loginTestPassword)
	require.Equal(t, http.StatusOK, loginRec.Code, "precondition: login works while the tenant is active")
	var refreshToken string
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == "refresh_token" {
			refreshToken = c.Value
		}
	}
	require.NotEmpty(t, refreshToken)
	f.setStatus(t, f.tenant.ID, tenancy.StatusSuspended)

	rec := f.refresh(t, refreshToken)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	resp := decodeAuth(t, rec)
	assert.Equal(t, models.CodeWorkspaceSuspended, resp.Code)
	assert.Contains(t, resp.Message, "suspended")
	assert.Empty(t, resp.Token, "no new access token may be issued")
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "refresh_token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared, "the refresh cookie must be cleared so the browser stops retrying")
}

func TestRefreshSession_PlatformAdminWithUnservableHomeTenantStillRefreshes_DB(t *testing.T) {
	f := newLoginFixture(t)
	withAdminEmailDomain(t, "example.com")
	owner := f.seedUnservableOwner(t)
	admin := f.seedHomeIdentity(t, owner.ID, "platform-admin")
	require.NoError(t, f.cp.AddPlatformAdmin(context.Background(), admin.ID))
	refreshToken, _, err := issueRefreshToken(context.Background(), f.cp, admin.ID)
	require.NoError(t, err)

	rec := f.refresh(t, refreshToken)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.NotEmpty(t, decodeAuth(t, rec).Token)
}

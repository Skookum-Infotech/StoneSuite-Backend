//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/tenancy"
)

// lifecycleHarness is a TenantOps with only the control plane wired — no
// Provisioner, Cloudflare or R2 client — plus a seeded platform-admin identity.
type lifecycleHarness struct {
	h       *TenantOps
	cp      *tenancy.ControlPlane
	adminID string
}

func newLifecycleHarness(t *testing.T) *lifecycleHarness {
	t.Helper()
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// IsPlatformAdmin re-checks the email domain on every call.
	origDomain := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "example.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = origDomain })

	homeTenantID := seedSAMLTestTenant(t, cp)
	admin, err := cp.CreateSSOIdentity(ctx, homeTenantID, "platform-admin-"+suffix+"@example.com", "Admin", "entra", "subject-"+suffix)
	require.NoError(t, err)
	require.NoError(t, cp.AddPlatformAdmin(ctx, admin.ID))
	isAdmin, err := cp.IsPlatformAdmin(ctx, admin.ID)
	require.NoError(t, err)
	require.True(t, isAdmin, "the seeded identity must pass the platform-admin check, or every test below is vacuous")

	return &lifecycleHarness{
		h:       &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)},
		cp:      cp,
		adminID: admin.ID,
	}
}

func (lh *lifecycleHarness) seedCustomer(t *testing.T) *tenancy.Tenant {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := lh.cp.CreateTenant(context.Background(), "lifecycle-"+suffix, "Lifecycle "+suffix, false)
	require.NoError(t, err)
	return tenant
}

// seedOwner creates the platform-owner tenant and removes it when the test
// ends: is_platform_owner is a database-enforced singleton.
func (lh *lifecycleHarness) seedOwner(t *testing.T) *tenancy.Tenant {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	owner, err := lh.cp.CreateTenant(context.Background(), "lifecycle-owner-"+suffix, "Lifecycle Owner", true)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lh.cp.Pool().Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, owner.ID)
	})
	return owner
}

func (lh *lifecycleHarness) post(t *testing.T, actorID, tenantID, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/platform/tenants/"+tenantID+"/"+action, strings.NewReader(body))
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: actorID})
	rec := httptest.NewRecorder()
	lh.h.TenantLifecycle(rec, req.WithContext(ctx))
	return rec
}

func purgeBody(slug string) string {
	b, _ := json.Marshal(map[string]string{"confirmSlug": slug})
	return string(b)
}

func (lh *lifecycleHarness) mustTenant(t *testing.T, id string) *tenancy.Tenant {
	t.Helper()
	got, err := lh.cp.TenantByID(context.Background(), id)
	require.NoError(t, err)
	return got
}

func TestTenantLifecycle_Purge_RemovesNeverProvisionedTenant_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	tenant := lh.seedCustomer(t)

	rec := lh.post(t, lh.adminID, tenant.ID, "purge", purgeBody(tenant.Slug))

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	_, err := lh.cp.TenantByID(context.Background(), tenant.ID)
	assert.ErrorIs(t, err, tenancy.ErrTenantNotFound)

	var n int
	require.NoError(t, lh.cp.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM platform_audit_logs
		 WHERE action = 'tenant.purge' AND tenant_id IS NULL AND details->>'slug' = $1 AND details->>'tenantId' = $2`,
		tenant.Slug, tenant.ID).Scan(&n))
	assert.Equal(t, 1, n, "a purge leaves one audit row that names the tenant it removed")
}

func TestTenantLifecycle_Purge_RefusedCases_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	tenant := lh.seedCustomer(t)

	tests := []struct {
		name   string
		actor  string
		id     string
		body   string
		status int
	}{
		{"wrong slug", lh.adminID, tenant.ID, purgeBody("not-it"), http.StatusBadRequest},
		{"empty body", lh.adminID, tenant.ID, "", http.StatusBadRequest},
		{"malformed body", lh.adminID, tenant.ID, "{", http.StatusBadRequest},
		{"unknown tenant", lh.adminID, "00000000-0000-0000-0000-000000000000", purgeBody("x"), http.StatusNotFound},
		{"not a platform admin", "00000000-0000-0000-0000-000000000001", tenant.ID, purgeBody(tenant.Slug), http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := lh.post(t, tc.actor, tc.id, "purge", tc.body)

			assert.Equal(t, tc.status, rec.Code, "body=%s", rec.Body.String())
			_, err := lh.cp.TenantByID(context.Background(), tenant.ID)
			assert.NoError(t, err, "a refused purge must leave the tenant in place")
		})
	}
}

func TestTenantLifecycle_PlatformOwnerIsProtected_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	owner := lh.seedOwner(t)

	for _, action := range []string{"suspend", "delete", "force-delete", "purge"} {
		t.Run(action, func(t *testing.T) {
			rec := lh.post(t, lh.adminID, owner.ID, action, purgeBody(owner.Slug))

			assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), "platform owner",
				"the 403 must come from the owner guard, not from the platform-admin check")
			got := lh.mustTenant(t, owner.ID)
			assert.Equal(t, owner.Status, got.Status, "the owner tenant's status must not change")
		})
	}
}

// With no Provisioner wired, a provisioned tenant's database cannot be dropped.
// The request must fail *before* the tenant is marked deleted, or a
// half-purged tenant would sit there unservable with its data intact.
func TestTenantLifecycle_Purge_AbortsBeforeAnyChangeWhenTeardownUnavailable_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	tenant := lh.seedCustomer(t)
	require.NoError(t, lh.cp.SetTenantProvisioned(context.Background(), tenant.ID, "tenant_purge_test_unused", "", 1))
	before := lh.mustTenant(t, tenant.ID)

	rec := lh.post(t, lh.adminID, tenant.ID, "purge", purgeBody(tenant.Slug))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	after := lh.mustTenant(t, tenant.ID)
	assert.Equal(t, before.Status, after.Status, "status must be untouched")
}

func TestTenantLifecycle_SuspendThenRestore_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	tenant := lh.seedCustomer(t)
	require.NoError(t, lh.cp.SetTenantStatus(context.Background(), tenant.ID, tenancy.StatusActive))

	rec := lh.post(t, lh.adminID, tenant.ID, "suspend", "")
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, tenancy.StatusSuspended, lh.mustTenant(t, tenant.ID).Status)

	rec = lh.post(t, lh.adminID, tenant.ID, "restore", "")
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, tenancy.StatusActive, lh.mustTenant(t, tenant.ID).Status)
}

func TestTenantLifecycle_UnknownTenantIs404_DB(t *testing.T) {
	lh := newLifecycleHarness(t)

	rec := lh.post(t, lh.adminID, "00000000-0000-0000-0000-000000000000", "suspend", "")

	assert.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
}

func TestListTenants_ExposesPlatformOwnerFlagAndBucket_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	owner := lh.seedOwner(t)
	customer := lh.seedCustomer(t)
	require.NoError(t, lh.cp.SetTenantR2Bucket(context.Background(), customer.ID, "ss-"+customer.Slug))

	req := httptest.NewRequest(http.MethodGet, "/api/platform/tenants", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: lh.adminID})
	rec := httptest.NewRecorder()
	lh.h.ListTenants(rec, req.WithContext(ctx))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp struct {
		Tenants []struct {
			ID              string `json:"id"`
			IsPlatformOwner bool   `json:"isPlatformOwner"`
			R2Bucket        string `json:"r2Bucket"`
		} `json:"tenants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	byID := map[string]struct {
		owner  bool
		bucket string
	}{}
	for _, row := range resp.Tenants {
		byID[row.ID] = struct {
			owner  bool
			bucket string
		}{row.IsPlatformOwner, row.R2Bucket}
	}
	assert.True(t, byID[owner.ID].owner, "the platform owner must be flagged")
	assert.False(t, byID[customer.ID].owner)
	assert.Equal(t, "ss-"+customer.Slug, byID[customer.ID].bucket)
}

// recordingDBProvider stands in for the Postgres host so a purge can be driven
// end to end without dropping a real database.
type recordingDBProvider struct{ dropped []string }

func (p *recordingDBProvider) CreateDatabase(context.Context, string) error { return nil }
func (p *recordingDBProvider) DSNFor(string) (string, error)                { return "", nil }
func (p *recordingDBProvider) DropDatabase(_ context.Context, name string) error {
	p.dropped = append(p.dropped, name)
	return nil
}

func (lh *lifecycleHarness) withProvider() *recordingDBProvider {
	prov := &recordingDBProvider{}
	lh.h.Prov = provisioning.New(nil, prov, nil, nil)
	return prov
}

func TestTenantLifecycle_Purge_DropsTheProvisionedDatabase_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	prov := lh.withProvider()
	tenant := lh.seedCustomer(t)
	dbName := fmt.Sprintf("tenant_purge_test_%d", time.Now().UnixNano())
	require.NoError(t, lh.cp.SetTenantProvisioned(context.Background(), tenant.ID, dbName, "", 1))

	rec := lh.post(t, lh.adminID, tenant.ID, "purge", purgeBody(tenant.Slug))

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, []string{dbName}, prov.dropped)
	_, err := lh.cp.TenantByID(context.Background(), tenant.ID)
	assert.ErrorIs(t, err, tenancy.ErrTenantNotFound)
}

// A db_name that isn't a provisioned tenant database must be refused up front,
// not after the tenant has been marked deleted and its bucket destroyed.
func TestTenantLifecycle_Purge_RefusesANonTenantDatabaseNameBeforeAnyChange_DB(t *testing.T) {
	lh := newLifecycleHarness(t)
	prov := lh.withProvider()
	tenant := lh.seedCustomer(t)
	require.NoError(t, lh.cp.SetTenantProvisioned(context.Background(), tenant.ID, "postgres", "", 1))
	before := lh.mustTenant(t, tenant.ID)

	rec := lh.post(t, lh.adminID, tenant.ID, "purge", purgeBody(tenant.Slug))

	assert.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
	assert.Empty(t, prov.dropped, "no database may be dropped")
	assert.Equal(t, before.Status, lh.mustTenant(t, tenant.ID).Status, "status must be untouched")
}

//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/tenancy"
)

// testCustomerTenantPool connects to the tenant-schema test database used by
// this package's dbtest suite (same TEST_DATABASE_URL crmactivity's own
// store_test.go relies on). Skips cleanly when unset.
func testCustomerTenantPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, dsn
}

// seedServableCustomerTestTenant creates a tenant whose db_connection_ref
// points at the tenant-schema test database, so Router.PoolFor (via the
// default PlainDSNResolver) connects to the same DB the test itself seeds
// rows into.
func seedServableCustomerTestTenant(t *testing.T, cp *tenancy.ControlPlane, dsn string) *tenancy.Tenant {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(ctx, "cust-portal-test-"+suffix, "Customer Portal Test Tenant", false)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "cust_portal_test_db", dsn, 1))
	got, err := cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)
	require.True(t, got.Servable(), "seeded tenant must be Servable()")
	return got
}

// seedCustomerRecord inserts a minimal live `customer` CRM record, mirroring
// crmactivity's seedCustomer helper.
func seedCustomerRecord(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	require.NoError(t, pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID))
	var custID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_id`,
		custTypeID, "Test Customer "+suffix).Scan(&custID))
	return custID
}

// seedActiveCustomerIdentity inserts an active customer_identities row with
// the given plaintext password bcrypt-hashed.
func seedActiveCustomerIdentity(t *testing.T, pool *pgxpool.Pool, customerID int, email, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	require.NoError(t, err)
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO customer_identities (customer_id, email, password_hash, full_name, status)
		VALUES ($1, $2, $3, 'Test Customer', 'active') RETURNING id`,
		customerID, email, string(hash)).Scan(&id))
	return id
}

// seedInvitedCustomerIdentity inserts an 'invited' customer_identities row
// with an invite token, mirroring the state PortalInvite would create.
func seedInvitedCustomerIdentity(t *testing.T, pool *pgxpool.Pool, customerID int, email, token string, expiry time.Time) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO customer_identities (customer_id, email, status, invite_token, invite_token_expiry)
		VALUES ($1, $2, 'invited', $3, $4) RETURNING id`,
		customerID, email, token, expiry).Scan(&id))
	return id
}

func loginBody(tenantSlug, email, password string) *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`{"tenantSlug":%q,"email":%q,"password":%q}`, tenantSlug, email, password))
}

func TestCustomerAuthOps_Login_HappyPath_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custID := seedCustomerRecord(t, pool)
	email := fmt.Sprintf("customer-%d@example.com", time.Now().UnixNano())
	seedActiveCustomerIdentity(t, pool, custID, email, "correct-password")

	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/login", loginBody(tenant.Slug, email, "correct-password"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.Token)

	var sawCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "customer_auth_token" && c.Value != "" {
			sawCookie = true
		}
	}
	assert.True(t, sawCookie, "expected customer_auth_token cookie to be set")
}

func TestCustomerAuthOps_Login_WrongPassword_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custID := seedCustomerRecord(t, pool)
	email := fmt.Sprintf("customer-%d@example.com", time.Now().UnixNano())
	seedActiveCustomerIdentity(t, pool, custID, email, "correct-password")

	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/login", loginBody(tenant.Slug, email, "wrong-password"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestCustomerAuthOps_Login_UnknownTenant_DB confirms an unknown tenant slug
// gets the same generic "invalid email or password" message as a bad
// password, never a distinguishable 404 — enumeration-safety.
func TestCustomerAuthOps_Login_UnknownTenant_DB(t *testing.T) {
	withTestJWTSecret(t)
	cp := newSAMLTestControlPlane(t)
	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/login", loginBody("no-such-tenant-slug", "a@example.com", "whatever"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var resp struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Invalid email or password.", resp.Message)
}

func TestCustomerAuthOps_Login_DisabledAccount_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custID := seedCustomerRecord(t, pool)
	email := fmt.Sprintf("customer-%d@example.com", time.Now().UnixNano())
	seedActiveCustomerIdentity(t, pool, custID, email, "correct-password")
	_, err := pool.Exec(context.Background(), `UPDATE customer_identities SET status = 'disabled' WHERE email = $1`, email)
	require.NoError(t, err)

	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/login", loginBody(tenant.Slug, email, "correct-password"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestCustomerAuthOps_AcceptInvite_HappyPath_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custID := seedCustomerRecord(t, pool)
	email := fmt.Sprintf("customer-%d@example.com", time.Now().UnixNano())
	token := fmt.Sprintf("invite-token-%d", time.Now().UnixNano())
	seedInvitedCustomerIdentity(t, pool, custID, email, token, time.Now().Add(time.Hour))

	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))
	body := strings.NewReader(fmt.Sprintf(`{"tenantSlug":%q,"token":%q,"password":"a-new-password"}`, tenant.Slug, token))
	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/accept-invite", body)
	rec := httptest.NewRecorder()
	h.AcceptInvite(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// The activated account can now log in with the password just set.
	loginReq := httptest.NewRequest(http.MethodPost, "/api/customer/auth/login", loginBody(tenant.Slug, email, "a-new-password"))
	loginRec := httptest.NewRecorder()
	h.Login(loginRec, loginReq)
	assert.Equal(t, http.StatusOK, loginRec.Code)
}

func TestCustomerAuthOps_AcceptInvite_ExpiredToken_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custID := seedCustomerRecord(t, pool)
	email := fmt.Sprintf("customer-%d@example.com", time.Now().UnixNano())
	token := fmt.Sprintf("invite-token-%d", time.Now().UnixNano())
	seedInvitedCustomerIdentity(t, pool, custID, email, token, time.Now().Add(-time.Hour))

	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))
	body := strings.NewReader(fmt.Sprintf(`{"tenantSlug":%q,"token":%q,"password":"a-new-password"}`, tenant.Slug, token))
	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/accept-invite", body)
	rec := httptest.NewRecorder()
	h.AcceptInvite(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

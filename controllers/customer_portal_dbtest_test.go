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

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// loginAndGetToken logs a seeded active customer identity in via the real
// CustomerAuthOps.Login handler and returns the issued JWT, so tests exercise
// the same token shape production requests would carry.
func loginAndGetToken(t *testing.T, cp *tenancy.ControlPlane, tenantSlug, email, password string) string {
	t.Helper()
	h := NewCustomerAuthOps(cp, tenancy.NewRouter(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/customer/auth/login", loginBody(tenantSlug, email, password))
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)
	return resp.Token
}

// customerPortalChain mirrors main.go's customerChain (minus rate limiting,
// which isn't under test here): RequireCustomerAuth -> tenancy resolution.
func customerPortalChain(cp *tenancy.ControlPlane) func(http.HandlerFunc) http.Handler {
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))
	return func(h http.HandlerFunc) http.Handler {
		return middleware.RequireCustomerAuth(resolver.CustomerMiddleware(h))
	}
}

func TestCustomerPortalOps_CreateNote_And_ListMyNotes_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custID := seedCustomerRecord(t, pool)
	email := fmt.Sprintf("portal-%d@example.com", time.Now().UnixNano())
	seedActiveCustomerIdentity(t, pool, custID, email, "correct-password")
	token := loginAndGetToken(t, cp, tenant.Slug, email, "correct-password")

	chain := customerPortalChain(cp)
	portal := NewCustomerPortalOps()

	createReq := httptest.NewRequest(http.MethodPost, "/api/customer/notes", strings.NewReader(`{"body":"I have an issue with X."}`))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRec := httptest.NewRecorder()
	chain(portal.CreateNote).ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	listReq := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRec := httptest.NewRecorder()
	chain(portal.ListMyNotes).ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)

	var resp struct {
		Notes []struct {
			Body string `json:"body"`
		} `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &resp))
	require.Len(t, resp.Notes, 1)
	assert.Equal(t, "I have an issue with X.", resp.Notes[0].Body)
}

// TestCustomerPortalOps_ListMyNotes_NeverCrossesCustomers is the IDOR-style
// guarantee the whole customer-note feature depends on: customer A's session
// must never surface customer B's notes, even within the same tenant.
func TestCustomerPortalOps_ListMyNotes_NeverCrossesCustomers(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	custA := seedCustomerRecord(t, pool)
	emailA := fmt.Sprintf("a-%d@example.com", time.Now().UnixNano())
	seedActiveCustomerIdentity(t, pool, custA, emailA, "password-a")
	tokenA := loginAndGetToken(t, cp, tenant.Slug, emailA, "password-a")

	custB := seedCustomerRecord(t, pool)
	emailB := fmt.Sprintf("b-%d@example.com", time.Now().UnixNano())
	seedActiveCustomerIdentity(t, pool, custB, emailB, "password-b")
	tokenB := loginAndGetToken(t, cp, tenant.Slug, emailB, "password-b")

	chain := customerPortalChain(cp)
	portal := NewCustomerPortalOps()

	submit := func(token, body string) {
		req := httptest.NewRequest(http.MethodPost, "/api/customer/notes", strings.NewReader(fmt.Sprintf(`{"body":%q}`, body)))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		chain(portal.CreateNote).ServeHTTP(rec, req)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}
	submit(tokenA, "Note from A")
	submit(tokenB, "Note from B")

	listAs := func(token string) []string {
		req := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		chain(portal.ListMyNotes).ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var resp struct {
			Notes []struct {
				Body string `json:"body"`
			} `json:"notes"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		out := make([]string, len(resp.Notes))
		for i, n := range resp.Notes {
			out[i] = n.Body
		}
		return out
	}

	assert.Equal(t, []string{"Note from A"}, listAs(tokenA))
	assert.Equal(t, []string{"Note from B"}, listAs(tokenB))
}

// TestCustomerPortalOps_CreateNote_RejectsStaffToken confirms a staff JWT can
// never reach the customer-note submission endpoint end-to-end (through the
// real resolver chain, not just the middleware unit test).
func TestCustomerPortalOps_CreateNote_RejectsStaffToken(t *testing.T) {
	withTestJWTSecret(t)
	cp := newSAMLTestControlPlane(t)
	tenantID := seedSAMLTestTenant(t, cp)
	identity, err := cp.CreateIdentity(context.Background(), tenantID, fmt.Sprintf("staff-%d@example.com", time.Now().UnixNano()), "", "Staff User", true)
	require.NoError(t, err)
	staffToken, err := generateTenantJWT(identity.ID, identity.Email, identity.TenantID, "", time.Hour)
	require.NoError(t, err)

	chain := customerPortalChain(cp)
	portal := NewCustomerPortalOps()

	req := httptest.NewRequest(http.MethodPost, "/api/customer/notes", strings.NewReader(`{"body":"should never land"}`))
	req.Header.Set("Authorization", "Bearer "+staffToken)
	rec := httptest.NewRecorder()
	chain(portal.CreateNote).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

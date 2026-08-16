package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantOps_Identify_NoDomain covers the fast path that never touches the
// database: an email with nothing usable after '@' (or no body at all)
// normalizes to an empty domain (tenancy.NormalizeEmailDomain), so Identify
// must resolve to "password" without calling h.CP -- exercised here with a
// nil CP to prove it.
func TestTenantOps_Identify_NoDomain(t *testing.T) {
	h := &TenantOps{}

	tests := []struct {
		name string
		body string
	}{
		{"empty email", `{"email":""}`},
		{"whitespace email", `{"email":"   "}`},
		{"trailing at-sign", `{"email":"user@"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()

			assert.NotPanics(t, func() { h.Identify(rr, req) })
			require.Equal(t, http.StatusOK, rr.Code)
			assert.JSONEq(t, `{"success":true,"method":"password"}`, rr.Body.String())
		})
	}
}

// TestTenantOps_Identify_InvalidBody covers malformed JSON -- a client bug
// unrelated to any particular email, so a 400 here reveals nothing about
// account existence.
func TestTenantOps_Identify_InvalidBody(t *testing.T) {
	h := &TenantOps{}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(`not-json`))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Identify(rr, req) })
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

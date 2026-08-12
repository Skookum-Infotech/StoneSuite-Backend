package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSAMLAuthOps_Exchange_ValidatesBodyBeforeTouchingControlPlane exercises
// every failure that happens before Exchange ever calls h.cp (nil on this
// handler, so a bug that reordered validation after the DB call would panic
// these tests instead of just failing an assertion).
func TestSAMLAuthOps_Exchange_ValidatesBodyBeforeTouchingControlPlane(t *testing.T) {
	h := &SAMLAuthOps{}

	tests := []struct {
		name string
		body string
	}{
		{"invalid json body", `{`},
		{"missing code field", `{}`},
		{"blank code field", `{"code":"   "}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/exchange", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			assert.NotPanics(t, func() { h.Exchange(rr, req) })
			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	}
}

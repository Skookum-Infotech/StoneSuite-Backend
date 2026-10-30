package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOwnBodyCapRoute(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPut, "/api/tenant/company-profile/logo", true},
		{http.MethodPost, "/api/tenant/records/abc/emails", true},
		{http.MethodPut, "/api/internal/email/inbound/ref1/attachments/att1", true},
		{http.MethodPost, "/api/tenant/company-profile/logo", false},
		{http.MethodGet, "/api/tenant/records/abc/emails", false},
		{http.MethodPost, "/api/tenant/records/abc/attachments", false},
		{http.MethodPost, "/api/auth/tenant-login", false},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, ownBodyCapRoute(httptest.NewRequest(tt.method, tt.path, nil)))
		})
	}
}

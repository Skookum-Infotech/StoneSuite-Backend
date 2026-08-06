package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDashboardOps_RequiresAuth proves every dashboard handler rejects an
// unauthenticated request before touching the tenant pool, mirroring
// TestQuoteOps_RequiresAuth (controllers/quote_test.go).
func TestDashboardOps_RequiresAuth(t *testing.T) {
	h := NewDashboardOps()
	handlers := map[string]http.HandlerFunc{
		"ListWidgets":      h.ListWidgets,
		"SavePreferences":  h.SavePreferences,
		"ResetPreferences": h.ResetPreferences,
		"GetConfig":        h.GetConfig,
		"SetConfig":        h.SetConfig,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets", nil)
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

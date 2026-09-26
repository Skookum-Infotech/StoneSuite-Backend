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

	"stonesuite-backend/aisettings"
	"stonesuite-backend/middleware"
)

func TestAIOpsStatus_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/ai/status", nil)
	w := httptest.NewRecorder()

	h.Status(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAIOpsUpdateSettings_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/api/tenant/ai/settings", nil)
	w := httptest.NewRecorder()

	h.UpdateSettings(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAIOpsGetPlatformSettings_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/platform/ai/settings", nil)
	w := httptest.NewRecorder()

	h.GetPlatformSettings(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAIOpsGetPlatformSettings_NonAdminRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, &fakePlatformAdminChecker{isAdmin: false}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/platform/ai/settings", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "u1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.GetPlatformSettings(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestAIOpsUpdatePlatformSettings_NonAdminRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, &fakePlatformAdminChecker{isAdmin: false}, nil)
	req := httptest.NewRequest(http.MethodPut, "/api/platform/ai/settings", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "u1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.UpdatePlatformSettings(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestWriteAssistantDisabled(t *testing.T) {
	tests := []struct {
		name       string
		status     aisettings.Status
		wantReason string
		wantMsg    string
	}{
		{"platform off", aisettings.Status{PlatformEnabled: false, TenantEnabled: true}, "platform", msgDisabledByPlatform},
		{"both off: platform wins", aisettings.Status{}, "platform", msgDisabledByPlatform},
		{"tenant off", aisettings.Status{PlatformEnabled: true, TenantEnabled: false}, "tenant", msgDisabledByTenant},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeAssistantDisabled(w, tt.status)

			assert.Equal(t, http.StatusForbidden, w.Code)
			var got map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, false, got["success"])
			assert.Equal(t, codeAssistantDisabled, got["code"])
			assert.Equal(t, tt.wantReason, got["reason"])
			assert.Equal(t, tt.wantMsg, got["message"])
		})
	}
}

func TestDecodeEnabledBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantOK      bool
		wantEnabled bool
		wantMsg     string
	}{
		{"true", `{"enabled":true}`, true, true, ""},
		{"false", `{"enabled":false}`, true, false, ""},
		{"missing field", `{}`, false, false, msgEnabledRequired},
		{"misspelled field", `{"enable":true}`, false, false, msgEnabledRequired},
		{"wrong type", `{"enabled":"yes"}`, false, false, "Invalid request body."},
		{"malformed", `{`, false, false, "Invalid request body."},
		{"too large", `{"enabled":true,"pad":"` + strings.Repeat("x", maxSettingsBodyBytes) + `"}`, false, false, "Invalid request body."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(tt.body))

			enabled, ok := decodeEnabledBody(w, r)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantEnabled, enabled)
			if !tt.wantOK {
				assert.Equal(t, http.StatusBadRequest, w.Code)
				assert.Contains(t, w.Body.String(), tt.wantMsg)
			}
		})
	}
}

// A bad body on the platform PUT is a 400 before any DB write: the handler
// here has a nil cpPool, so reaching the write would panic.
func TestAIOpsUpdatePlatformSettings_BadBodyRejected(t *testing.T) {
	for name, body := range map[string]string{
		"missing":       `{}`,
		"misspelled":    `{"enable":false}`,
		"wrong type":    `{"enabled":1}`,
		"not an object": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			h := NewAIOps(nil, nil, nil, &fakePlatformAdminChecker{isAdmin: true}, nil)
			req := httptest.NewRequest(http.MethodPut, "/api/platform/ai/settings", strings.NewReader(body))
			ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "u1"})
			w := httptest.NewRecorder()

			h.UpdatePlatformSettings(w, req.WithContext(ctx))

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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

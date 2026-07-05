package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
)

func TestNarrowestScope_PicksMostRestrictiveAmongGranted(t *testing.T) {
	tests := []struct {
		name      string
		decisions []authz.Decision
		wantScope authz.Scope
		wantOK    bool
	}{
		{
			name:      "no grants at all -> denied",
			decisions: []authz.Decision{{Allowed: false}, {Allowed: false}, {Allowed: false}},
			wantOK:    false,
		},
		{
			name: "single grant wins",
			decisions: []authz.Decision{
				{Allowed: true, Scope: authz.ScopeAll},
				{Allowed: false},
				{Allowed: false},
			},
			wantScope: authz.ScopeAll,
			wantOK:    true,
		},
		{
			name: "own beats team and all -- most restrictive wins",
			decisions: []authz.Decision{
				{Allowed: true, Scope: authz.ScopeAll},
				{Allowed: true, Scope: authz.ScopeTeam},
				{Allowed: true, Scope: authz.ScopeOwn},
			},
			wantScope: authz.ScopeOwn,
			wantOK:    true,
		},
		{
			name: "ungranted resource is excluded, not treated as deny-all",
			decisions: []authz.Decision{
				{Allowed: true, Scope: authz.ScopeAll},
				{Allowed: false}, // caller has zero access to this resource
			},
			wantScope: authz.ScopeAll,
			wantOK:    true,
		},
		{
			name:      "empty input -> denied",
			decisions: nil,
			wantOK:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, ok := narrowestScope(tt.decisions)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && scope != tt.wantScope {
				t.Fatalf("scope = %q, want %q", scope, tt.wantScope)
			}
		})
	}
}

func TestAIOpsAsk_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/ask", nil)
	w := httptest.NewRecorder()

	h.Ask(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAIOpsReindex_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/reindex", nil)
	w := httptest.NewRecorder()

	h.Reindex(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

type fakePlatformAdminChecker struct {
	isAdmin bool
	err     error
}

func (f *fakePlatformAdminChecker) IsPlatformAdmin(_ context.Context, _ string) (bool, error) {
	return f.isAdmin, f.err
}

func TestAIOpsReindexHelp_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/platform/ai/reindex-help", nil)
	w := httptest.NewRecorder()

	h.ReindexHelp(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAIOpsReindexHelp_NonAdminRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, &fakePlatformAdminChecker{isAdmin: false}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/platform/ai/reindex-help", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "u1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ReindexHelp(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"stonesuite-backend/ai"
	"stonesuite-backend/middleware"
)

func TestSplitGranted(t *testing.T) {
	grants := ai.Grants{"lead": ai.ScopeAll, "customer": ai.ScopeOwn, "prospect": "team"}
	granted, denied := splitGranted(grants, []string{"customer", "lead", "prospect"})
	if strings.Join(granted, ",") != "customer,lead" || strings.Join(denied, ",") != "prospect" {
		t.Fatalf("granted=%v denied=%v (a retired scope must not count as a grant)", granted, denied)
	}
}

func TestNoAccessSentence(t *testing.T) {
	tests := map[string][]string{
		"":                                       nil,
		"You don't have access to lead records.": {"lead"},
		"You don't have access to lead or customer records.":            {"lead", "customer"},
		"You don't have access to lead, prospect, or customer records.": {"lead", "prospect", "customer"},
	}
	for want, denied := range tests {
		if got := noAccessSentence(denied); got != want {
			t.Errorf("noAccessSentence(%v) = %q, want %q", denied, got, want)
		}
	}
}

func TestConversationTitleIsRuneSafe(t *testing.T) {
	q := strings.Repeat("é", maxConversationTitleLength+5)
	got := conversationTitleFromQuestion(q)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") {
		t.Fatalf("title must cut on a character boundary: %q", got)
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

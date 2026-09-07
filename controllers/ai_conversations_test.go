package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These mirror ai_test.go's TestAIOpsAsk_UnauthenticatedRejected pattern: no
// pool/tenant context is available in a unit test, so every handler must
// reject before ever touching one — proven by nil dependencies never being
// dereferenced.

func TestConversationOpsCreate_UnauthenticatedRejected(t *testing.T) {
	h := NewConversationOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/conversations", nil)
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestConversationOpsList_UnauthenticatedRejected(t *testing.T) {
	h := NewConversationOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/ai/conversations", nil)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestConversationOpsGet_UnauthenticatedRejected(t *testing.T) {
	h := NewConversationOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/ai/conversations/x", nil)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestConversationOpsDelete_UnauthenticatedRejected(t *testing.T) {
	h := NewConversationOps()
	req := httptest.NewRequest(http.MethodDelete, "/api/tenant/ai/conversations/x", nil)
	w := httptest.NewRecorder()

	h.Delete(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

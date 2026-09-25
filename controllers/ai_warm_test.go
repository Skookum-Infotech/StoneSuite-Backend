package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAssistantWarmer_SingleFlight(t *testing.T) {
	now := time.Unix(0, 0)
	w := newAssistantWarmer(func() time.Time { return now })

	if !w.start() {
		t.Fatalf("first start() must succeed")
	}
	if w.start() {
		t.Fatalf("start() while in flight must return false")
	}
	w.finish(true)
	if w.start() {
		t.Fatalf("start() within warmCooldown of a success must return false")
	}
}

func TestAssistantWarmer_CooldownExpiresThenAllowsAgain(t *testing.T) {
	now := time.Unix(0, 0)
	w := newAssistantWarmer(func() time.Time { return now })

	if !w.start() {
		t.Fatalf("first start() must succeed")
	}
	w.finish(true)

	now = now.Add(warmCooldown - time.Second)
	if w.start() {
		t.Fatalf("start() just under warmCooldown must still return false")
	}

	now = now.Add(2 * time.Second)
	if !w.start() {
		t.Fatalf("start() once warmCooldown has elapsed must return true")
	}
}

func TestAssistantWarmer_FailedWarmUpDoesNotBlockRetry(t *testing.T) {
	now := time.Unix(0, 0)
	w := newAssistantWarmer(func() time.Time { return now })

	if !w.start() {
		t.Fatalf("first start() must succeed")
	}
	w.finish(false)

	if !w.start() {
		t.Fatalf("a failed warm-up must not start a cooldown")
	}
}

func TestAIOpsWarm_UnauthenticatedRejected(t *testing.T) {
	h := NewAIOps(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/warm", nil)
	w := httptest.NewRecorder()

	h.Warm(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

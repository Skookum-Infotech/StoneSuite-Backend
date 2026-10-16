package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
)

type fakeAutomationSwitch struct {
	state    automation.PlatformState
	setCalls int
	setBy    string
	setErr   error
	stateErr error
}

func (f *fakeAutomationSwitch) State(context.Context) (automation.PlatformState, error) {
	return f.state, f.stateErr
}

func (f *fakeAutomationSwitch) Set(_ context.Context, enabled bool, by string) error {
	f.setCalls++
	f.setBy = by
	if f.setErr != nil {
		return f.setErr
	}
	f.state.Enabled = enabled
	return nil
}

type fakeAutomationMirror struct {
	configured bool
	err        error
	calls      int
	last       bool
}

func (f *fakeAutomationMirror) Configured() bool { return f.configured }
func (f *fakeAutomationMirror) Set(_ context.Context, enabled bool) error {
	f.calls++
	f.last = enabled
	return f.err
}

func automationAdminReq(method, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/platform/automation", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: "u1", Email: "admin@example.com"})
	return req.WithContext(ctx)
}

func decodeAutomationResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	return m
}

func TestAutomationPlatform_AuthGate(t *testing.T) {
	for _, call := range []string{"get", "update"} {
		t.Run(call+" unauthenticated", func(t *testing.T) {
			sw := &fakeAutomationSwitch{}
			h := NewAutomationPlatformOps(&fakePlatformAdminChecker{isAdmin: true}, sw, &fakeAutomationMirror{}, false)
			req := httptest.NewRequest(http.MethodPut, "/api/platform/automation", strings.NewReader(`{"enabled":true}`))
			w := httptest.NewRecorder()
			if call == "get" {
				h.Get(w, req)
			} else {
				h.Update(w, req)
			}
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Zero(t, sw.setCalls)
		})
		t.Run(call+" non-admin", func(t *testing.T) {
			sw := &fakeAutomationSwitch{}
			mirror := &fakeAutomationMirror{configured: true}
			h := NewAutomationPlatformOps(&fakePlatformAdminChecker{isAdmin: false}, sw, mirror, false)
			w := httptest.NewRecorder()
			if call == "get" {
				h.Get(w, automationAdminReq(http.MethodGet, ""))
			} else {
				h.Update(w, automationAdminReq(http.MethodPut, `{"enabled":true}`))
			}
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Zero(t, sw.setCalls, "a non-admin must never write the switch")
			assert.Zero(t, mirror.calls, "a non-admin must never reach Cloudflare")
		})
	}
	t.Run("admin check error", func(t *testing.T) {
		h := NewAutomationPlatformOps(&fakePlatformAdminChecker{err: errors.New("db")}, &fakeAutomationSwitch{}, &fakeAutomationMirror{}, false)
		w := httptest.NewRecorder()
		h.Get(w, automationAdminReq(http.MethodGet, ""))
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

func TestAutomationPlatform_Get(t *testing.T) {
	sw := &fakeAutomationSwitch{state: automation.PlatformState{Enabled: true, UpdatedBy: "a@example.com"}}
	h := NewAutomationPlatformOps(&fakePlatformAdminChecker{isAdmin: true}, sw, &fakeAutomationMirror{configured: true}, false)
	w := httptest.NewRecorder()

	h.Get(w, automationAdminReq(http.MethodGet, ""))

	require.Equal(t, http.StatusOK, w.Code)
	m := decodeAutomationResp(t, w)
	assert.Equal(t, true, m["enabled"])
	assert.Equal(t, "a@example.com", m["updatedBy"])
	assert.Equal(t, true, m["kvConfigured"])
	assert.Contains(t, m["message"], "vendor-payment")
}

func TestAutomationPlatform_Update(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		mirror     *fakeAutomationMirror
		setErr     error
		wantCode   int
		wantSetN   int
		wantKVN    int
		wantSynced any // nil = field absent
		wantOK     bool
	}{
		{"enable and sync", `{"enabled":true}`, &fakeAutomationMirror{configured: true}, nil, http.StatusOK, 1, 1, true, true},
		{"disable and sync", `{"enabled":false}`, &fakeAutomationMirror{configured: true}, nil, http.StatusOK, 1, 1, true, true},
		{"kv not configured: db only", `{"enabled":true}`, &fakeAutomationMirror{}, nil, http.StatusOK, 1, 0, nil, true},
		{"kv failure is 502 but db is saved", `{"enabled":true}`, &fakeAutomationMirror{configured: true, err: errors.New("cf down")}, nil, http.StatusBadGateway, 1, 1, false, false},
		{"db failure skips kv", `{"enabled":true}`, &fakeAutomationMirror{configured: true}, errors.New("db"), http.StatusInternalServerError, 1, 0, nil, false},
		{"missing enabled", `{}`, &fakeAutomationMirror{configured: true}, nil, http.StatusBadRequest, 0, 0, nil, false},
		{"wrong type", `{"enabled":"yes"}`, &fakeAutomationMirror{configured: true}, nil, http.StatusBadRequest, 0, 0, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw := &fakeAutomationSwitch{setErr: tt.setErr}
			h := NewAutomationPlatformOps(&fakePlatformAdminChecker{isAdmin: true}, sw, tt.mirror, false)
			w := httptest.NewRecorder()

			h.Update(w, automationAdminReq(http.MethodPut, tt.body))

			assert.Equal(t, tt.wantCode, w.Code)
			assert.Equal(t, tt.wantSetN, sw.setCalls)
			assert.Equal(t, tt.wantKVN, tt.mirror.calls)
			if tt.wantSetN > 0 && tt.setErr == nil {
				assert.Equal(t, "admin@example.com", sw.setBy, "audit trail records the acting admin")
			}
			m := decodeAutomationResp(t, w)
			assert.Equal(t, tt.wantOK, m["success"])
			if tt.wantSynced == nil {
				assert.NotContains(t, m, "kvSynced")
			} else {
				assert.Equal(t, tt.wantSynced, m["kvSynced"])
			}
			if tt.wantKVN > 0 {
				assert.Equal(t, strings.Contains(tt.body, "true"), tt.mirror.last)
			}
		})
	}
}

func TestAutomationPlatform_RequireKV(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		mirror   *fakeAutomationMirror
		wantCode int
		wantSetN int
	}{
		{"enable refused without kv", `{"enabled":true}`, &fakeAutomationMirror{}, http.StatusConflict, 0},
		{"disable allowed without kv", `{"enabled":false}`, &fakeAutomationMirror{}, http.StatusOK, 1},
		{"enable allowed with kv", `{"enabled":true}`, &fakeAutomationMirror{configured: true}, http.StatusOK, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw := &fakeAutomationSwitch{}
			h := NewAutomationPlatformOps(&fakePlatformAdminChecker{isAdmin: true}, sw, tt.mirror, true)
			w := httptest.NewRecorder()

			h.Update(w, automationAdminReq(http.MethodPut, tt.body))

			assert.Equal(t, tt.wantCode, w.Code)
			assert.Equal(t, tt.wantSetN, sw.setCalls, "a refused enable must not touch the database")
		})
	}
}

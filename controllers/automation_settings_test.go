package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
)

type fakeSettingsStore struct {
	s   AutomationSettings
	err error
}

func (f *fakeSettingsStore) Get(context.Context) (AutomationSettings, error) { return f.s, f.err }

func (f *fakeSettingsStore) SetKillSwitch(_ context.Context, on bool) (AutomationSettings, error) {
	if f.err != nil {
		return AutomationSettings{}, f.err
	}
	f.s.KillSwitch = on
	now := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	f.s.UpdatedAt = &now
	return f.s, nil
}

type fakePlatform struct {
	on  bool
	err error
}

func (f fakePlatform) Enabled(context.Context) (bool, error) { return f.on, f.err }

func settingsOps(store *fakeSettingsStore, platform platformSwitchReader, allowed map[authz.Action]bool) *AutomationSettingsOps {
	return &AutomationSettingsOps{platform: platform, resolve: func(*http.Request) (automationSettingsEnv, error) {
		return automationSettingsEnv{store: store, check: func(_ context.Context, _ string, a authz.Action) (authz.Decision, error) {
			return authz.Decision{Allowed: allowed[a], Scope: authz.ScopeAll}, nil
		}}, nil
	}}
}

func settingsReq(method, body string, authed bool) *http.Request {
	req := httptest.NewRequest(method, "/api/tenant/automation/settings", strings.NewReader(body))
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	return req
}

func defaultsStore() *fakeSettingsStore {
	return &fakeSettingsStore{s: defaultAutomationSettings()}
}

func TestAutomationSettings_Get(t *testing.T) {
	read := map[authz.Action]bool{authz.ActionRead: true}
	tests := []struct {
		name     string
		allowed  map[authz.Action]bool
		authed   bool
		store    *fakeSettingsStore
		platform platformSwitchReader
		want     int
		contains []string
	}{
		{"unauthenticated", read, false, defaultsStore(), fakePlatform{}, http.StatusUnauthorized, nil},
		{"denied", nil, true, defaultsStore(), fakePlatform{}, http.StatusForbidden, nil},
		{"platform off", read, true, defaultsStore(), fakePlatform{on: false}, http.StatusOK,
			[]string{`"platformEnabled":false`, `"killSwitch":false`, `"dailyExternalCap":200`, `"perContactWeeklyCap":3`}},
		{"platform on", read, true, defaultsStore(), fakePlatform{on: true}, http.StatusOK, []string{`"platformEnabled":true`}},
		{"control plane error", read, true, defaultsStore(), fakePlatform{err: errors.New("cp down")}, http.StatusOK,
			[]string{`"platformEnabled":null`}},
		{"store error", read, true, &fakeSettingsStore{err: errors.New("boom")}, fakePlatform{}, http.StatusInternalServerError, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			settingsOps(tt.store, tt.platform, tt.allowed).Get(rec, settingsReq(http.MethodGet, "", tt.authed))
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			for _, c := range tt.contains {
				assert.Contains(t, rec.Body.String(), c)
			}
			assert.NotContains(t, rec.Body.String(), "updatedBy")
		})
	}
}

func TestAutomationSettings_Put(t *testing.T) {
	configure := map[authz.Action]bool{authz.ActionConfigure: true}
	tests := []struct {
		name    string
		allowed map[authz.Action]bool
		authed  bool
		body    string
		want    int
		wantOn  bool
	}{
		{"unauthenticated", configure, false, `{"killSwitch":true}`, http.StatusUnauthorized, false},
		{"read only is denied", map[authz.Action]bool{authz.ActionRead: true}, true, `{"killSwitch":true}`, http.StatusForbidden, false},
		{"invalid json", configure, true, `{`, http.StatusBadRequest, false},
		{"missing field", configure, true, `{}`, http.StatusBadRequest, false},
		{"non-bool", configure, true, `{"killSwitch":"yes"}`, http.StatusBadRequest, false},
		{"unknown field", configure, true, `{"killSwitch":true,"dailyExternalCap":1}`, http.StatusBadRequest, false},
		{"allowed on", configure, true, `{"killSwitch":true}`, http.StatusOK, true},
		{"allowed off", configure, true, `{"killSwitch":false}`, http.StatusOK, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := defaultsStore()
			rec := httptest.NewRecorder()
			settingsOps(store, fakePlatform{on: true}, tt.allowed).Put(rec, settingsReq(http.MethodPut, tt.body, tt.authed))
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantOn, store.s.KillSwitch)
			if tt.want == http.StatusOK {
				assert.Contains(t, rec.Body.String(), `"platformEnabled":true`)
			}
		})
	}
}

func TestAutomationSettings_PutStoreError(t *testing.T) {
	rec := httptest.NewRecorder()
	store := &fakeSettingsStore{err: errors.New("boom")}
	settingsOps(store, fakePlatform{}, map[authz.Action]bool{authz.ActionConfigure: true}).
		Put(rec, settingsReq(http.MethodPut, `{"killSwitch":true}`, true))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

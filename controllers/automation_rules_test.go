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

type fakeRuleStore struct {
	rules map[string]*AutomationRule
	clock time.Time
}

func newFakeRuleStore() *fakeRuleStore {
	return &fakeRuleStore{
		rules: map[string]*AutomationRule{"ar_dunning": {RuleKey: "ar_dunning", Name: "Invoice payment reminders", Audience: "external"}},
		clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	}
}

func (f *fakeRuleStore) List(context.Context) ([]AutomationRule, error) {
	out := []AutomationRule{}
	for _, r := range f.rules {
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeRuleStore) SetEnabled(_ context.Context, key string, enabled bool) (AutomationRule, error) {
	r, ok := f.rules[key]
	if !ok {
		return AutomationRule{}, ErrAutomationRuleNotFound
	}
	if enabled && !r.Enabled {
		f.clock = f.clock.Add(time.Hour)
		t := f.clock
		r.EnabledAt = &t
	}
	r.Enabled = enabled
	return *r, nil
}

func ruleOps(store *fakeRuleStore, allowed map[authz.Action]bool) *AutomationRuleOps {
	return &AutomationRuleOps{resolve: func(*http.Request) (automationRuleEnv, error) {
		return automationRuleEnv{store: store, check: func(_ context.Context, _ string, a authz.Action) (authz.Decision, error) {
			return authz.Decision{Allowed: allowed[a], Scope: authz.ScopeAll}, nil
		}}, nil
	}}
}

func putRule(h http.HandlerFunc, key, body string, authed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/tenant/automation/rules/"+key, strings.NewReader(body))
	req.SetPathValue("key", key)
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAutomationRules_Authorization(t *testing.T) {
	grantAll := map[authz.Action]bool{authz.ActionRead: true, authz.ActionConfigure: true}
	tests := []struct {
		name    string
		allowed map[authz.Action]bool
		authed  bool
		key     string
		body    string
		want    int
	}{
		{"unauthenticated", grantAll, false, "ar_dunning", `{"enabled":true}`, http.StatusUnauthorized},
		{"no permission", map[authz.Action]bool{authz.ActionRead: true}, true, "ar_dunning", `{"enabled":true}`, http.StatusForbidden},
		{"unknown key", grantAll, true, "nope", `{"enabled":true}`, http.StatusNotFound},
		{"invalid json", grantAll, true, "ar_dunning", `{`, http.StatusBadRequest},
		{"missing enabled", grantAll, true, "ar_dunning", `{}`, http.StatusBadRequest},
		{"non-bool enabled", grantAll, true, "ar_dunning", `{"enabled":"yes"}`, http.StatusBadRequest},
		{"ok", grantAll, true, "ar_dunning", `{"enabled":true}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := putRule(ruleOps(newFakeRuleStore(), tt.allowed).SetEnabled, tt.key, tt.body, tt.authed)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}

func TestAutomationRules_ListRequiresRead(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/automation/rules", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "i"}))
	rec := httptest.NewRecorder()
	ruleOps(newFakeRuleStore(), nil).List(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = httptest.NewRecorder()
	ruleOps(newFakeRuleStore(), map[authz.Action]bool{authz.ActionRead: true}).List(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ruleKey":"ar_dunning"`)
}

func TestAutomationRules_EnabledAtSemantics(t *testing.T) {
	store := newFakeRuleStore()
	h := ruleOps(store, map[authz.Action]bool{authz.ActionConfigure: true}).SetEnabled

	require.Equal(t, http.StatusOK, putRule(h, "ar_dunning", `{"enabled":true}`, true).Code)
	first := *store.rules["ar_dunning"].EnabledAt

	require.Equal(t, http.StatusOK, putRule(h, "ar_dunning", `{"enabled":true}`, true).Code)
	assert.Equal(t, first, *store.rules["ar_dunning"].EnabledAt, "enable twice keeps first enabledAt")

	require.Equal(t, http.StatusOK, putRule(h, "ar_dunning", `{"enabled":false}`, true).Code)
	assert.False(t, store.rules["ar_dunning"].Enabled)
	assert.Equal(t, first, *store.rules["ar_dunning"].EnabledAt, "disable leaves enabledAt")

	require.Equal(t, http.StatusOK, putRule(h, "ar_dunning", `{"enabled":true}`, true).Code)
	assert.True(t, store.rules["ar_dunning"].EnabledAt.After(first), "re-enable resets enabledAt")
}

func TestAutomationRules_ExposeMachineFields(t *testing.T) {
	planned := time.Date(2026, 11, 2, 13, 45, 0, 0, time.UTC)
	store := newFakeRuleStore()
	store.rules["ar_dunning"].IsSystem = true
	store.rules["ar_dunning"].PreviewSupported = true
	store.rules["ar_dunning"].LastPlannedAt = &planned

	req := httptest.NewRequest(http.MethodGet, "/api/tenant/automation/rules", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: "i"}))
	rec := httptest.NewRecorder()
	ruleOps(store, map[authz.Action]bool{authz.ActionRead: true}).List(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"isSystem":true`)
	assert.Contains(t, body, `"previewSupported":true`)
	assert.Contains(t, body, `"lastPlannedAt":"2026-11-02T13:45:00Z"`)
}

func TestAutomationRules_AnomalyScanNeedsPlatform(t *testing.T) {
	configure := map[authz.Action]bool{authz.ActionConfigure: true}
	tests := []struct {
		name     string
		platform platformSwitchReader
		key      string
		body     string
		want     int
		wantMsg  string
	}{
		{"enable blocked while platform off", fakePlatform{on: false}, "anomaly_scan", `{"enabled":true}`,
			http.StatusConflict, "super admin must turn on Automation"},
		{"enable allowed while platform on", fakePlatform{on: true}, "anomaly_scan", `{"enabled":true}`, http.StatusOK, ""},
		{"disable always allowed", fakePlatform{on: false}, "anomaly_scan", `{"enabled":false}`, http.StatusOK, ""},
		{"fails closed when the switch is unreadable", fakePlatform{err: errors.New("cp down")}, "anomaly_scan",
			`{"enabled":true}`, http.StatusServiceUnavailable, "Could not confirm"},
		{"other rules are not gated", fakePlatform{on: false}, "ar_dunning", `{"enabled":true}`, http.StatusOK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeRuleStore()
			store.rules["anomaly_scan"] = &AutomationRule{RuleKey: "anomaly_scan", Name: "Anomaly detection", Audience: "internal"}
			ops := ruleOps(store, configure)
			ops.platform = tt.platform
			rec := putRule(ops.SetEnabled, tt.key, tt.body, true)
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.wantMsg != "" {
				assert.Contains(t, rec.Body.String(), tt.wantMsg)
			}
			if tt.want != http.StatusOK {
				assert.False(t, store.rules[tt.key].Enabled, "a refused request must not change the rule")
			}
		})
	}
}

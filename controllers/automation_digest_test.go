package controllers

import (
	"context"
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

type fakeDigestStore struct {
	userIDs map[string]string
	prefs   map[string]automation.DigestPrefs
	loadErr error
	saveErr error
	saved   int
}

func newFakeDigestStore() *fakeDigestStore {
	return &fakeDigestStore{userIDs: map[string]string{"ident-1": "user-1"}, prefs: map[string]automation.DigestPrefs{}}
}

func (f *fakeDigestStore) UserID(_ context.Context, identityID string) (string, error) {
	return f.userIDs[identityID], nil
}

func (f *fakeDigestStore) Load(_ context.Context, userID string) (automation.DigestPrefs, error) {
	if f.loadErr != nil {
		return automation.DigestPrefs{}, f.loadErr
	}
	if p, ok := f.prefs[userID]; ok {
		return p, nil
	}
	return automation.DefaultDigestPrefs(), nil
}

func (f *fakeDigestStore) Save(_ context.Context, userID string, p automation.DigestPrefs) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if err := p.Validate(); err != nil {
		return err
	}
	f.prefs[userID] = p
	f.saved++
	return nil
}

func digestOps(store digestPrefsStore) *AutomationDigestOps {
	return &AutomationDigestOps{resolve: func(*http.Request) (digestPrefsStore, error) { return store, nil }}
}

func digestRequest(method, body string, identity string) *http.Request {
	req := httptest.NewRequest(method, "/api/tenant/automation/digest/me", strings.NewReader(body))
	if identity != "" {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{ID: identity}))
	}
	return req
}

func TestDigestPrefs_RequireLogin(t *testing.T) {
	ops := digestOps(newFakeDigestStore())
	for name, h := range map[string]http.HandlerFunc{"get": ops.Get, "put": ops.Put} {
		rec := httptest.NewRecorder()
		h(rec, digestRequest(http.MethodPut, `{"enabled":true}`, ""))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
	}
}

func TestDigestPrefs_UnknownTenantUserIsAnError(t *testing.T) {
	rec := httptest.NewRecorder()
	digestOps(newFakeDigestStore()).Get(rec, digestRequest(http.MethodGet, "", "stranger"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
}

func TestDigestPrefs_GetReturnsDefaultsForANewUser(t *testing.T) {
	rec := httptest.NewRecorder()
	digestOps(newFakeDigestStore()).Get(rec, digestRequest(http.MethodGet, "", "ident-1"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"enabled":false`)
	assert.Contains(t, rec.Body.String(), `"sendHour":8`)
}

func TestDigestPrefs_PutPatchesOnlyWhatIsSent(t *testing.T) {
	store := newFakeDigestStore()
	ops := digestOps(store)

	rec := httptest.NewRecorder()
	ops.Put(rec, digestRequest(http.MethodPut, `{"enabled":true,"sendHour":14}`, "ident-1"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	ops.Put(rec, digestRequest(http.MethodPut, `{"includeVendorBills":false}`, "ident-1"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := store.prefs["user-1"]
	assert.True(t, got.Enabled, "an earlier choice survives a later partial update")
	assert.Equal(t, 14, got.SendHour)
	assert.False(t, got.IncludeVendorBills)
	assert.True(t, got.IncludeApprovals)
}

func TestDigestPrefs_PutWritesOnlyTheCallersRow(t *testing.T) {
	store := newFakeDigestStore()
	store.userIDs["ident-2"] = "user-2"
	digestOps(store).Put(httptest.NewRecorder(), digestRequest(http.MethodPut, `{"enabled":true}`, "ident-2"))
	assert.Contains(t, store.prefs, "user-2")
	assert.NotContains(t, store.prefs, "user-1")
}

func TestDigestPrefs_PutRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{`},
		{"empty object", `{}`},
		{"unknown field", `{"enabled":true,"userId":"user-2"}`},
		{"hour before the window", `{"sendHour":6}`},
		{"hour after the window", `{"sendHour":18}`},
		{"fractional hour", `{"sendHour":9.5}`},
		{"string flag", `{"enabled":"yes"}`},
		{"every section off while on", `{"enabled":true,"includeInstalls":false,"includeVendorBills":false,"includeApprovals":false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeDigestStore()
			rec := httptest.NewRecorder()
			digestOps(store).Put(rec, digestRequest(http.MethodPut, tc.body, "ident-1"))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"success":false`)
			assert.Zero(t, store.saved, "a refused request must not write")
		})
	}
}

func TestDigestPrefs_StoreFailuresAre500(t *testing.T) {
	store := newFakeDigestStore()
	store.saveErr = errors.New("db down")
	rec := httptest.NewRecorder()
	digestOps(store).Put(rec, digestRequest(http.MethodPut, `{"enabled":true}`, "ident-1"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	store = newFakeDigestStore()
	store.loadErr = errors.New("db down")
	rec = httptest.NewRecorder()
	digestOps(store).Get(rec, digestRequest(http.MethodGet, "", "ident-1"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

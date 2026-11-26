package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
)

type fakeSnapshotReader struct {
	configured bool
	val        []byte
	found      bool
	err        error
	gotKey     string
}

func (f *fakeSnapshotReader) Configured() bool { return f.configured }
func (f *fakeSnapshotReader) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.gotKey = key
	return f.val, f.found, f.err
}

func systemHealthReq(withUser bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/platform/system-health", nil)
	if !withUser {
		return req
	}
	ctx := context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: "u1", Email: "admin@example.com"})
	return req.WithContext(ctx)
}

func TestSystemHealthGet(t *testing.T) {
	snapshot := `{"version":1,"checkedAt":"2026-11-25T14:00:00.000Z","problems":[]}`
	tests := []struct {
		name           string
		admin          *fakePlatformAdminChecker
		kv             *fakeSnapshotReader
		withUser       bool
		wantStatus     int
		wantConfigured bool
		wantSnapshot   string
		wantMessage    string
	}{
		{"no user", &fakePlatformAdminChecker{isAdmin: true}, &fakeSnapshotReader{}, false, http.StatusUnauthorized, false, "", ""},
		{"not platform admin", &fakePlatformAdminChecker{isAdmin: false}, &fakeSnapshotReader{configured: true}, true, http.StatusForbidden, false, "", ""},
		{"admin check fails", &fakePlatformAdminChecker{err: errors.New("db")}, &fakeSnapshotReader{configured: true}, true, http.StatusInternalServerError, false, "", ""},
		{"kv not configured", &fakePlatformAdminChecker{isAdmin: true}, &fakeSnapshotReader{configured: false}, true, http.StatusOK, false, "null", msgSystemHealthNotConfigured},
		{"no snapshot yet", &fakePlatformAdminChecker{isAdmin: true}, &fakeSnapshotReader{configured: true}, true, http.StatusOK, true, "null", msgSystemHealthNoSnapshot},
		{"snapshot returned as-is", &fakePlatformAdminChecker{isAdmin: true}, &fakeSnapshotReader{configured: true, found: true, val: []byte(snapshot)}, true, http.StatusOK, true, snapshot, ""},
		{"kv read fails", &fakePlatformAdminChecker{isAdmin: true}, &fakeSnapshotReader{configured: true, err: errors.New("403")}, true, http.StatusBadGateway, false, "", ""},
		{"snapshot not json", &fakePlatformAdminChecker{isAdmin: true}, &fakeSnapshotReader{configured: true, found: true, val: []byte("{oops")}, true, http.StatusBadGateway, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewSystemHealthPlatformOps(tt.admin, tt.kv)
			rec := httptest.NewRecorder()
			h.Get(rec, systemHealthReq(tt.withUser))

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus != http.StatusOK {
				return
			}
			var body struct {
				Success    bool            `json:"success"`
				Configured bool            `json:"configured"`
				Snapshot   json.RawMessage `json:"snapshot"`
				Message    string          `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.True(t, body.Success)
			assert.Equal(t, tt.wantConfigured, body.Configured)
			assert.JSONEq(t, tt.wantSnapshot, string(body.Snapshot))
			assert.Equal(t, tt.wantMessage, body.Message)
			if tt.wantConfigured {
				assert.Equal(t, automation.WatchdogSnapshotKey, tt.kv.gotKey)
			}
		})
	}
}

func TestSystemHealthGetNilKV(t *testing.T) {
	var kv *automation.KVMirror // not configured
	h := NewSystemHealthPlatformOps(&fakePlatformAdminChecker{isAdmin: true}, kv)
	rec := httptest.NewRecorder()
	h.Get(rec, systemHealthReq(true))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"configured":false`)
}

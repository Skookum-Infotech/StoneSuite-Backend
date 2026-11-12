package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
)

func TestSessionStatus(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	tests := []struct {
		name                      string
		replacedAt, endsAt, ended *time.Time
		want                      string
	}{
		{"live", nil, nil, nil, SessionActive},
		{"logged out", nil, nil, &past, SessionEnded},
		{"replaced, in handover", &past, &future, nil, SessionReplacing},
		{"replaced, handover over", &past, &past, nil, SessionEnded},
		{"replaced at the exact end", &past, &now, nil, SessionEnded},
		{"replaced without end", &past, nil, nil, SessionEnded},
		{"replaced then logged out", &past, &future, &past, SessionEnded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sessionStatus(tt.replacedAt, tt.endsAt, tt.ended, now))
		})
	}
}

func TestDecideSession(t *testing.T) {
	tests := []struct {
		status, method string
		want           guardDecision
	}{
		{SessionActive, http.MethodGet, guardAllow},
		{SessionActive, http.MethodPost, guardAllow},
		{SessionReplacing, http.MethodGet, guardAllow},
		{SessionReplacing, http.MethodHead, guardAllow},
		{SessionReplacing, http.MethodPost, guardRejectWrite},
		{SessionReplacing, http.MethodPatch, guardRejectWrite},
		{SessionReplacing, http.MethodDelete, guardRejectWrite},
		{SessionEnded, http.MethodGet, guardRejectEnded},
		{SessionEnded, http.MethodPost, guardRejectEnded},
		{"unknown", http.MethodGet, guardRejectEnded},
	}
	for _, tt := range tests {
		t.Run(tt.status+" "+tt.method, func(t *testing.T) {
			assert.Equal(t, tt.want, decideSession(tt.status, tt.method))
		})
	}
}

// fakeSessions serves fixed sessions to the guard.
type fakeSessions struct {
	byID  map[string]*AuthSession
	err   error
	calls int
}

func (f *fakeSessions) SessionByID(_ context.Context, id string) (*AuthSession, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	s, ok := f.byID[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

func TestSessionGuardCheck(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	sessions := map[string]*AuthSession{
		"live":      {ID: "live"},
		"replacing": {ID: "replacing", ReplacedAt: &past, EndsAt: &future},
		"ended":     {ID: "ended", ReplacedAt: &past, EndsAt: &past},
	}
	tests := []struct {
		name, sid, method string
		loadErr           error
		wantPass          bool
		wantStatus        int
	}{
		{"no sid (pre-session token)", "", http.MethodPost, nil, true, 0},
		{"live session", "live", http.MethodPost, nil, true, 0},
		{"handover read", "replacing", http.MethodGet, nil, true, 0},
		{"handover write", "replacing", http.MethodPost, nil, false, http.StatusConflict},
		{"ended", "ended", http.MethodGet, nil, false, http.StatusUnauthorized},
		{"unknown session", "gone", http.MethodGet, nil, false, http.StatusUnauthorized},
		{"lookup failure: reads pass", "live", http.MethodGet, errors.New("db down"), true, 0},
		{"lookup failure: writes refused", "live", http.MethodPost, errors.New("db down"), false, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newSessionGuard(&fakeSessions{byID: sessions, err: tt.loadErr})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/api/tenant/x", nil)
			pass := g.check(rec, req, middleware.UserContextPayload{ID: "i", SessionID: tt.sid})
			assert.Equal(t, tt.wantPass, pass)
			if tt.wantPass {
				return
			}
			assert.Equal(t, tt.wantStatus, rec.Code)
			var body models.APIResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.False(t, body.Success)
			want := CodeSessionReplaced
			if tt.loadErr != nil {
				want = CodeSessionUnavailable
			}
			assert.Equal(t, want, body.Code)
		})
	}
}

func TestSessionGuardCachesAndForgets(t *testing.T) {
	f := &fakeSessions{byID: map[string]*AuthSession{"live": {ID: "live"}}}
	g := newSessionGuard(f)
	p := middleware.UserContextPayload{SessionID: "live"}
	for range 3 {
		assert.True(t, g.check(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), p))
	}
	assert.Equal(t, 1, f.calls, "state is cached between requests")
	g.Forget("live")
	g.check(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), p)
	assert.Equal(t, 2, f.calls, "Forget forces a reload")
}

func TestNilSessionGuardPasses(t *testing.T) {
	var g *SessionGuard
	assert.True(t, g.check(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil),
		middleware.UserContextPayload{SessionID: "any"}))
}

func TestSessionGuardRequire(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	g := newSessionGuard(&fakeSessions{byID: map[string]*AuthSession{
		"live":  {ID: "live"},
		"ended": {ID: "ended", EndedAt: &past},
	}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	tests := []struct {
		name string
		ctx  bool
		sid  string
		want int
	}{
		{"no auth context", false, "", http.StatusUnauthorized},
		{"live session reaches the handler", true, "live", http.StatusNoContent},
		{"ended session is refused", true, "ended", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", nil)
			if tt.ctx {
				req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
					middleware.UserContextPayload{ID: "i", SessionID: tt.sid}))
			}
			rec := httptest.NewRecorder()
			g.Require(next).ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

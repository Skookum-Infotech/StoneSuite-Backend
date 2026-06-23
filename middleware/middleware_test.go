package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		flyClient  string
		forwarded  string
		remoteAddr string
		want       string
	}{
		{"fly header wins", "203.0.113.5", "198.51.100.1", "10.0.0.1:1234", "203.0.113.5"},
		{"xff first hop when no fly", "", "198.51.100.1, 10.0.0.2", "10.0.0.1:1234", "198.51.100.1"},
		{"remoteaddr stripped of port", "", "", "192.0.2.9:55555", "192.0.2.9"},
		{"fly header trimmed", "  203.0.113.7 ", "", "10.0.0.1:1", "203.0.113.7"},
		{"remoteaddr without port returned as-is", "", "", "192.0.2.10", "192.0.2.10"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.flyClient != "" {
				r.Header.Set("Fly-Client-IP", tc.flyClient)
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			assert.Equal(t, tc.want, ClientIP(r))
		})
	}
}

func TestRecover_TurnsPanicInto500(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	// Must not propagate the panic.
	require.NotPanics(t, func() {
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "unexpected error")
	// The panic detail must never leak to the client.
	assert.NotContains(t, rec.Body.String(), "boom")
}

func TestRecover_PassesThroughNormalResponses(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestRequestLogger_SetsCorrelationID(t *testing.T) {
	var seenInHandler string
	handler := RequestLogger(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenInHandler = RequestIDFromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	header := rec.Header().Get(RequestIDHeader)
	assert.NotEmpty(t, header, "X-Request-ID header should be set")
	assert.Equal(t, header, seenInHandler, "context id must match the response header")
}

func TestRequestIDFromContext_AbsentReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", RequestIDFromContext(context.Background()))
}

func TestStatusRecorder_DefaultsTo200(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	_, _ = rec.Write([]byte("hi"))
	assert.Equal(t, http.StatusOK, rec.Status())
	assert.Equal(t, 2, rec.bytes)
}

func TestPerIP_ThrottlesAfterBurst(t *testing.T) {
	// rate 0 so the bucket never refills during the test; burst 3 means exactly
	// 3 requests succeed, the 4th is rejected with 429.
	rl := NewRateLimiter(context.Background(), 0, 3)
	handler := rl.PerIP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func() int {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", nil)
		r.Header.Set("Fly-Client-IP", "203.0.113.99")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, call())
	assert.Equal(t, http.StatusOK, call())
	assert.Equal(t, http.StatusOK, call())
	assert.Equal(t, http.StatusTooManyRequests, call(), "4th request from same IP should be throttled")
}

func TestPerIP_IndependentPerIP(t *testing.T) {
	rl := NewRateLimiter(context.Background(), 0, 1)
	handler := rl.PerIP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(ip string) int {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("Fly-Client-IP", ip)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, call("1.1.1.1"))
	assert.Equal(t, http.StatusTooManyRequests, call("1.1.1.1"))
	// A different IP has its own bucket and is unaffected.
	assert.Equal(t, http.StatusOK, call("2.2.2.2"))
}

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type observation struct {
	tenantID string
	status   int
}

type fakeObserver struct{ got []observation }

func (f *fakeObserver) Observe(tenantID string, status int, _ time.Duration) {
	f.got = append(f.got, observation{tenantID, status})
}

func TestNewRequestLogger_ObservesAuthenticatedOnly(t *testing.T) {
	obs := &fakeObserver{}
	authed := NewRequestLogger(obs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAttribution(r.Context(), UserContextPayload{ID: "id-1", TenantID: "tenant-1"})
		w.WriteHeader(http.StatusInternalServerError)
	}))
	authed.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tenant/x", nil))
	assert.Equal(t, []observation{{"tenant-1", http.StatusInternalServerError}}, obs.got)

	obs.got = nil
	anon := NewRequestLogger(obs)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	anon.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
	assert.Empty(t, obs.got)
}

func TestNewRequestLogger_NilObserverAndNoSlot(t *testing.T) {
	h := NewRequestLogger(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAttribution(r.Context(), UserContextPayload{TenantID: "t"})
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	// setAttribution outside a logger-wrapped chain is a harmless no-op.
	setAttribution(httptest.NewRequest(http.MethodGet, "/", nil).Context(), UserContextPayload{TenantID: "t"})
}

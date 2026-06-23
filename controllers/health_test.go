package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHealthz_AlwaysOK(t *testing.T) {
	h := NewHealthOps(func(context.Context) error { return errors.New("db down") })
	rec := httptest.NewRecorder()
	h.Healthz(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	// Liveness must not depend on the database probe.
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name  string
		ready func(context.Context) error
		want  int
	}{
		{"nil probe is ready", nil, http.StatusOK},
		{"healthy db", func(context.Context) error { return nil }, http.StatusOK},
		{"db down -> 503", func(context.Context) error { return errors.New("unreachable") }, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHealthOps(tc.ready)
			rec := httptest.NewRecorder()
			h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/api/readyz", nil))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

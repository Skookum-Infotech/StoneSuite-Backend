package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/payment"
	"stonesuite-backend/query"

	"github.com/stretchr/testify/assert"
)

func TestPaymentOps_RequiresAuth(t *testing.T) {
	h := NewPaymentOps()
	handlers := map[string]http.HandlerFunc{
		"Create": h.Create,
		"Get":    h.Get,
		"Update": h.Update,
		"Delete": h.Delete,
		"List":   h.List,
		"Search": h.Search,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/payments", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

func TestPaymentFail_MapsStoreErrorsToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not found", payment.ErrNotFound, http.StatusNotFound},
		{"invalid transition", payment.ErrInvalidTransition, http.StatusConflict},
		{"client error", payment.ClientError{Msg: "bad input"}, http.StatusBadRequest},
		{"invalid filter", &query.InvalidFilterError{Field: "x", Msg: "unknown field"}, http.StatusBadRequest},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			paymentFail(rr, tt.err, "server error")
			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/estimate"
	"stonesuite-backend/query"

	"github.com/stretchr/testify/assert"
)

func TestEstimateOps_RequiresAuth(t *testing.T) {
	h := NewEstimateOps()
	handlers := map[string]http.HandlerFunc{
		"Create":     h.Create,
		"Get":        h.Get,
		"Update":     h.Update,
		"Delete":     h.Delete,
		"List":       h.List,
		"Search":     h.Search,
		"Transition": h.Transition,
		"Approve":    h.Approve,
		"Audit":      h.Audit,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/estimates", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

func TestEstimateFail_MapsStoreErrorsToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not found", estimate.ErrNotFound, http.StatusNotFound},
		{"invalid transition", estimate.ErrInvalidTransition, http.StatusConflict},
		{"approval required", estimate.ErrApprovalRequired, http.StatusConflict},
		{"approval not required", estimate.ErrApprovalNotRequired, http.StatusConflict},
		{"not approver", estimate.ErrNotApprover, http.StatusForbidden},
		{"client error", estimate.ClientError{Msg: "bad input"}, http.StatusBadRequest},
		{"invalid filter", &query.InvalidFilterError{Field: "x", Msg: "unknown field"}, http.StatusBadRequest},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			estimateFail(rr, tt.err, "server error")
			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

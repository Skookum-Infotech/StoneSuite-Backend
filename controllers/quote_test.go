package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/query"
	"stonesuite-backend/quote"

	"github.com/stretchr/testify/assert"
)

func TestQuoteOps_RequiresAuth(t *testing.T) {
	h := NewQuoteOps()
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
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/quotes", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

func TestQuoteFail_MapsStoreErrorsToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not found", quote.ErrNotFound, http.StatusNotFound},
		{"invalid transition", quote.ErrInvalidTransition, http.StatusConflict},
		{"approval required", quote.ErrApprovalRequired, http.StatusConflict},
		{"approval not required", quote.ErrApprovalNotRequired, http.StatusConflict},
		{"not approver", quote.ErrNotApprover, http.StatusForbidden},
		{"client error", quote.ClientError{Msg: "bad input"}, http.StatusBadRequest},
		{"invalid filter", &query.InvalidFilterError{Field: "x", Msg: "unknown field"}, http.StatusBadRequest},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			quoteFail(rr, tt.err, "server error")
			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

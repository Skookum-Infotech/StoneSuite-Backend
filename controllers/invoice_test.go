package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
	"stonesuite-backend/query"

	"github.com/stretchr/testify/assert"
)

func TestInvoiceOps_RequiresAuth(t *testing.T) {
	h := NewInvoiceOps()
	handlers := map[string]http.HandlerFunc{
		"Create":        h.Create,
		"Get":           h.Get,
		"Update":        h.Update,
		"Delete":        h.Delete,
		"List":          h.List,
		"Search":        h.Search,
		"Transition":    h.Transition,
		"RecordPayment": h.RecordPayment,
		"Audit":         h.Audit,
		"Payments":      h.Payments,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/invoices", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

func TestInvoiceFail_MapsStoreErrorsToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not found", invoice.ErrNotFound, http.StatusNotFound},
		{"invalid transition", invoice.ErrInvalidTransition, http.StatusConflict},
		{"client error", invoice.ClientError{Msg: "bad input"}, http.StatusBadRequest},
		{"payment client error", payment.ClientError{Msg: "bad input"}, http.StatusBadRequest},
		{"invalid filter", &query.InvalidFilterError{Field: "x", Msg: "unknown field"}, http.StatusBadRequest},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			invoiceFail(rr, tt.err, "server error")
			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

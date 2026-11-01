package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/customertransactions"
)

func TestWriteCustomerTxError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		handled bool
		status  int
	}{
		{"nil", nil, false, http.StatusOK},
		{"invalid param", &customertransactions.InvalidParamError{Param: "type", Msg: "unknown"}, true, http.StatusBadRequest},
		{"wrapped invalid param", fmt.Errorf("x: %w", &customertransactions.InvalidParamError{Param: "q", Msg: "long"}), true, http.StatusBadRequest},
		{"customer not found", customertransactions.ErrCustomerNotFound, true, http.StatusNotFound},
		{"other", errors.New("boom"), true, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			assert.Equal(t, tt.handled, writeCustomerTxError(rec, tt.err))
			if tt.handled {
				assert.Equal(t, tt.status, rec.Code)
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}

func TestParseCustomerTxParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
		ok    bool
	}{
		{"empty", "", true},
		{"all valid", "type=invoice&status=SENT&from=2026-01-01&to=2026-10-31&q=INV&open=true&page=2&limit=50", true},
		{"bad from", "from=nope", false},
		{"bad to", "to=2026-13-45", false},
		{"bad open", "open=maybe", false},
		{"bad page", "page=abc", false},
		{"negative limit", "limit=-1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x?"+tt.query, nil)
			var p customertransactions.Params
			assert.Equal(t, tt.ok, parseCustomerTxParams(rec, req, &p))
			if !tt.ok {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
			}
		})
	}
}

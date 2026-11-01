package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/vendortransactions"
)

func TestWriteVendorTxError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		handled bool
		status  int
	}{
		{"nil", nil, false, http.StatusOK},
		{"invalid param", &vendortransactions.InvalidParamError{Param: "type", Msg: "unknown"}, true, http.StatusBadRequest},
		{"wrapped invalid param", fmt.Errorf("x: %w", &vendortransactions.InvalidParamError{Param: "cursor", Msg: "bad"}), true, http.StatusBadRequest},
		{"vendor not found", vendortransactions.ErrVendorNotFound, true, http.StatusNotFound},
		{"other", errors.New("boom"), true, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			assert.Equal(t, tt.handled, writeVendorTxError(rec, tt.err))
			if tt.handled {
				assert.Equal(t, tt.status, rec.Code)
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}

func TestParseVendorTxParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
		ok    bool
	}{
		{"empty", "", true},
		{"all valid", "type=vendor_bill&status=APPV&from=2026-01-01&to=2026-10-31&q=VBIL&open=true&cursor=abc&limit=50", true},
		{"bad from", "from=nope", false},
		{"bad to", "to=2026-13-45", false},
		{"bad open", "open=maybe", false},
		{"bad limit", "limit=abc", false},
		{"negative limit", "limit=-1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x?"+tt.query, nil)
			var p vendortransactions.Params
			assert.Equal(t, tt.ok, parseVendorTxParams(rec, req, &p))
			if !tt.ok {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
			}
		})
	}
}

func TestParseVendorTxParams_ReadsValues(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x?type=vendor_bill&status=APPV&q=VB&open=true&cursor=tok&limit=7&from=2026-01-02", nil)
	var p vendortransactions.Params
	require.True(t, parseVendorTxParams(rec, req, &p))
	assert.Equal(t, "vendor_bill", p.Type)
	assert.Equal(t, "APPV", p.Status)
	assert.Equal(t, "VB", p.Search)
	assert.True(t, p.OpenOnly)
	assert.Equal(t, "tok", p.Cursor)
	assert.Equal(t, 7, p.Limit)
	require.NotNil(t, p.From)
	assert.Nil(t, p.To)
}

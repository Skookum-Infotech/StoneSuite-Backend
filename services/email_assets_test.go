package services

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmailLogoHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	EmailLogoHandler(rec, httptest.NewRequest(http.MethodGet, EmailLogoPath, nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "\x89PNG", rec.Body.String()[:4])
}

func TestEmailIconHandler(t *testing.T) {
	tests := []struct {
		name     string
		wantCode int
	}{
		{"envelope.png", http.StatusOK},
		{"social-x.png", http.StatusOK},
		{"attachment-pdf.png", http.StatusOK},
		{"missing.png", http.StatusNotFound},
		{"..%2Fassets%2Femail-logo.png", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(EmailIconRoute, EmailIconHandler)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, EmailIconPathPrefix+tc.name, nil))
			assert.Equal(t, tc.wantCode, rec.Code)
			if tc.wantCode == http.StatusOK {
				assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
			}
		})
	}
}

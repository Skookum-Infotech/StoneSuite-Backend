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

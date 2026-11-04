package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/installschedule"
)

func TestWriteInstallError_InvalidTime(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	_, resolveErr := installschedule.ResolveInterval("2026-03-08T02:30", "2026-03-08T03:30", false, loc)
	require.Error(t, resolveErr)

	rec := httptest.NewRecorder()
	writeInstallError(rec, httptest.NewRequest(http.MethodPost, "/", nil), fmt.Errorf("create: %w", resolveErr))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body.Success)
	assert.Equal(t, installCodeInvalidTime, body.Code)
	assert.Equal(t, msgInstallInvalidTime, body.Message)
	assert.NotContains(t, rec.Body.String(), "installschedule:")
	assert.NotContains(t, rec.Body.String(), "America/Chicago")
}

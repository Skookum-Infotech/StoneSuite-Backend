package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/inventory"
)

func TestKitFail(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"missing kit reads Kit, not Item", inventory.ErrNotFound, http.StatusNotFound, "Kit not found."},
		{"wrapped missing kit", fmt.Errorf("load: %w", inventory.ErrNotFound), http.StatusNotFound, "Kit not found."},
		{"bad input is a 400 with its own words", inventory.ClientError{Msg: "A kit name is required."}, http.StatusBadRequest, "A kit name is required."},
		{"anything else is a 500 with the generic message", errors.New("boom"), http.StatusInternalServerError, "Failed to save kit."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			kitFail(rec, tc.err, "Failed to save kit.")

			assert.Equal(t, tc.wantStatus, rec.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, false, body["success"])
			assert.Equal(t, tc.wantMsg, body["message"])
		})
	}
}

// A same-named kit answers 409 with the kit it collided with, so the picker can
// point at it instead of only refusing.
func TestKitFail_DuplicateNameIs409WithTheExistingKit(t *testing.T) {
	rec := httptest.NewRecorder()
	kitFail(rec, &duplicate.Error{
		Entity: duplicate.EntityKit, Name: "kitchen",
		ExistingID: "7d0c1f2e-4a3b-4c5d-8e6f-0123456789ab", ExistingName: "Kitchen", ExistingStatus: "Inactive",
	}, "Failed to create kit.")

	assert.Equal(t, http.StatusConflict, rec.Code)
	var body struct {
		Message   string `json:"message"`
		Duplicate struct {
			Entity string `json:"entity"`
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"duplicate"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "kit", body.Duplicate.Entity)
	assert.Equal(t, "7d0c1f2e-4a3b-4c5d-8e6f-0123456789ab", body.Duplicate.ID)
	assert.Equal(t, "Kitchen", body.Duplicate.Name)
	assert.Equal(t, "Inactive", body.Duplicate.Status)
	assert.Contains(t, body.Message, "A kit named")
}

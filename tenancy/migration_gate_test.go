package tenancy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMigrationGate(t *testing.T) {
	var nilGate *MigrationGate
	assert.False(t, nilGate.Migrating("a"))
	nilGate.Begin("a")
	nilGate.Done("a")

	g := NewMigrationGate()
	g.Begin("a")
	assert.True(t, g.Migrating("a"))
	assert.False(t, g.Migrating("b"))
	g.Done("a")
	assert.False(t, g.Migrating("a"))
}

func TestRejectIfMigrating(t *testing.T) {
	g := NewMigrationGate()
	g.Begin("busy")
	tests := []struct {
		name     string
		gate     *MigrationGate
		tenant   string
		rejected bool
	}{
		{"migrating tenant", g, "busy", true},
		{"other tenant", g, "idle", false},
		{"nil gate", nil, "busy", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			got := rejectIfMigrating(rec, tc.gate, tc.tenant)
			assert.Equal(t, tc.rejected, got)
			if !tc.rejected {
				assert.Equal(t, http.StatusOK, rec.Code)
				return
			}
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Equal(t, "5", rec.Header().Get("Retry-After"))
			var body struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.False(t, body.Success)
			assert.Equal(t, migratingMessage, body.Message)
		})
	}
}

package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

func TestFailLoad(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			"malformed id is a 400, not an outage",
			&pgconn.PgError{Code: "22P02"},
			http.StatusBadRequest, "A value has an invalid format.",
		},
		{
			"malformed id wrapped by the store",
			fmt.Errorf("load vendor: %w", &pgconn.PgError{Code: "22P02"}),
			http.StatusBadRequest, "A value has an invalid format.",
		},
		{
			"numeric id out of range is a 400",
			&pgconn.PgError{Code: "22003"},
			http.StatusBadRequest, "A number is out of range.",
		},
		{
			"unexpected database error stays a 500 with the caller's message",
			&pgconn.PgError{Code: "XX000"},
			http.StatusInternalServerError, "Failed to load thing.",
		},
		{
			"non-database error stays a 500",
			errors.New("connection reset"),
			http.StatusInternalServerError, "Failed to load thing.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			failLoad(rec, tc.err, "Failed to load thing.")
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.JSONEq(t, fmt.Sprintf(`{"success":false,"message":%q}`, tc.wantMessage), rec.Body.String())
		})
	}
}

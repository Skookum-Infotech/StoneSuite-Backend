package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"stonesuite-backend/query"
)

// pgInvalidText is SQLSTATE 22P02: what Postgres raises when a path id such as
// "not-a-uuid" is cast to a uuid column.
const pgInvalidText = "22P02"

func TestPortalFail(t *testing.T) {
	const fallback = "Failed to load thing."
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			"malformed id is a 400, not an outage",
			&pgconn.PgError{Code: pgInvalidText},
			http.StatusBadRequest, "A value has an invalid format.",
		},
		{
			"malformed id wrapped the way a portal store wraps it",
			fmt.Errorf("portal get payment: %w", &pgconn.PgError{Code: pgInvalidText}),
			http.StatusBadRequest, "A value has an invalid format.",
		},
		{
			"malformed id wrapped twice",
			fmt.Errorf("load document: %w", fmt.Errorf("portal get invoice: %w", &pgconn.PgError{Code: pgInvalidText})),
			http.StatusBadRequest, "A value has an invalid format.",
		},
		{
			"numeric id out of range is a 400",
			&pgconn.PgError{Code: "22003"},
			http.StatusBadRequest, "A number is out of range.",
		},
		{
			"invalid filter key keeps its own message",
			&query.InvalidFilterError{},
			http.StatusBadRequest, (&query.InvalidFilterError{}).Error(),
		},
		{
			"unexpected database error stays a 500 with the handler's message",
			&pgconn.PgError{Code: "XX000"},
			http.StatusInternalServerError, fallback,
		},
		{
			"non-database error stays a 500",
			errors.New("connection reset"),
			http.StatusInternalServerError, fallback,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			portalFail(rec, tc.err, fallback)
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.JSONEq(t, fmt.Sprintf(`{"success":false,"message":%q}`, tc.wantMessage), rec.Body.String())
		})
	}
}

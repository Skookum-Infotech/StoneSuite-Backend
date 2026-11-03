package dberr

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

func TestMap(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantOK     bool
	}{
		{"unique", &pgconn.PgError{Code: codeUniqueViolation}, http.StatusConflict, true},
		{"foreign key", &pgconn.PgError{Code: codeForeignKeyViolation}, http.StatusConflict, true},
		{"check", &pgconn.PgError{Code: codeCheckViolation}, http.StatusBadRequest, true},
		{"not null", &pgconn.PgError{Code: codeNotNullViolation}, http.StatusBadRequest, true},
		{"too long", &pgconn.PgError{Code: codeStringTooLong}, http.StatusBadRequest, true},
		{"numeric range", &pgconn.PgError{Code: codeNumericOutOfRange}, http.StatusBadRequest, true},
		{"wrapped", fmt.Errorf("insert: %w", &pgconn.PgError{Code: codeUniqueViolation}), http.StatusConflict, true},
		{"unknown pg code", &pgconn.PgError{Code: "XX000"}, 0, false},
		{"non-pg error", errors.New("boom"), 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, msg, ok := Map(tc.err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantStatus, status)
			if ok {
				assert.NotEmpty(t, msg)
			}
		})
	}
}

// Package dberr turns the Postgres errors a bad request can trigger into an
// HTTP status and a safe message. Without it, a duplicate number or an
// over-long string reached the client as a generic 500, which tells the user
// nothing and reads as a server outage. Messages never echo Postgres's own
// text, which names tables, columns and values.
package dberr

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres SQLSTATE codes a client's input can cause.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
	codeNotNullViolation    = "23502"
	codeStringTooLong       = "22001"
	codeNumericOutOfRange   = "22003"
	codeInvalidText         = "22P02"
	codeInvalidDatetime     = "22007"
	codeDatetimeOutOfRange  = "22008"
)

// Map reports the HTTP status and message for a client-caused database
// error; ok is false for anything else (a real server error stays a 500).
func Map(err error) (status int, msg string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, "", false
	}
	switch pgErr.Code {
	case codeUniqueViolation:
		return http.StatusConflict, "A record with these values already exists.", true
	case codeForeignKeyViolation:
		return http.StatusConflict, "This refers to a record that does not exist, or is still in use elsewhere.", true
	case codeCheckViolation, codeNotNullViolation:
		return http.StatusBadRequest, "A value is missing or outside the allowed range.", true
	case codeStringTooLong:
		return http.StatusBadRequest, "A value is too long.", true
	case codeNumericOutOfRange:
		return http.StatusBadRequest, "A number is out of range.", true
	case codeInvalidText:
		return http.StatusBadRequest, "A value has an invalid format.", true
	case codeInvalidDatetime, codeDatetimeOutOfRange:
		return http.StatusBadRequest, "A date is invalid.", true
	}
	return 0, "", false
}

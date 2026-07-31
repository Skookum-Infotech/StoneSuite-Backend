package accountingperiod

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a period or fiscal year uuid matches no row.
var ErrNotFound = errors.New("accounting period not found")

// ErrNotConfigured is returned when an operation needs a fiscal calendar and
// the tenant has never run Setup.
var ErrNotConfigured = errors.New("fiscal calendar is not configured")

// ErrAlreadyConfigured is returned when Setup runs a second time.
var ErrAlreadyConfigured = errors.New("fiscal calendar is already configured")

// Sequencing sentinels. Each is surfaced wrapped in a ConflictError carrying a
// message that names the offending period, so callers can match on the
// sentinel with errors.Is while users read something actionable.
var (
	// ErrAlreadyClosed — closing a period that is already closed.
	ErrAlreadyClosed = errors.New("accounting period is already closed")
	// ErrAlreadyOpen — reopening a period that is already open.
	ErrAlreadyOpen = errors.New("accounting period is already open")
	// ErrPriorPeriodOpen — closing out of order would leave a hole in the
	// closed prefix that books_closed_through cannot represent.
	ErrPriorPeriodOpen = errors.New("an earlier accounting period is still open")
	// ErrLaterPeriodClosed — reopening runs strictly in reverse chronological
	// order, so a later closed period blocks it.
	ErrLaterPeriodClosed = errors.New("a later accounting period is closed")
	// ErrBeforeBasePeriod — periods before the go-live boundary stand for books
	// closed in the tenant's previous system and are never reopened.
	ErrBeforeBasePeriod = errors.New("accounting period precedes the base period")
)

// ClientError signals a caller-caused failure a controller maps to HTTP 400,
// mirroring chartofaccounts.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// ConflictError signals a state clash a controller maps to HTTP 409. It wraps
// one of the sequencing sentinels above so errors.Is still identifies the
// specific rule that was violated.
type ConflictError struct {
	Msg string
	err error
}

func (e ConflictError) Error() string { return e.Msg }

// Unwrap exposes the wrapped sentinel to errors.Is.
func (e ConflictError) Unwrap() error { return e.err }

// IsConflict reports whether err is a ConflictError.
func IsConflict(err error) bool {
	var ce ConflictError
	return errors.As(err, &ce)
}

// conflict builds a ConflictError around a sentinel.
func conflict(sentinel error, msg string) error {
	return ConflictError{Msg: msg, err: sentinel}
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (23505). Kept local to avoid a store->controllers import,
// mirroring chartofaccounts.isUniqueViolation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// nullableInt renders an unresolved employee id as SQL NULL rather than 0,
// which would violate the employee_id foreign key.
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

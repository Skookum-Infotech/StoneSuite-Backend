package companylocation

// errors.go — the package's error vocabulary and the PostgreSQL SQLSTATE
// predicates the store maps against. Kept local to the package rather than
// imported from another store package, matching inventory/errors.go's own
// rationale.

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when an id matches nothing live (or the row is
// soft-deleted). Controllers map it to 404.
var ErrNotFound = errors.New("company location not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// sqlState reports whether err is a PostgreSQL error carrying the given
// SQLSTATE code.
func sqlState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// isInvalidTextRepresentation reports whether err is Postgres rejecting a
// malformed uuid literal (22P02) — mapped to 404 rather than 500 so a
// garbled id reads the same as one that simply doesn't exist.
func isInvalidTextRepresentation(err error) bool { return sqlState(err, "22P02") }

// isUniqueViolation reports whether err is a unique-constraint violation
// (23505) — in practice, only reachable if two callers race to create a
// tenant's very first location in the same instant (see Create).
func isUniqueViolation(err error) bool { return sqlState(err, "23505") }

// nullableInt returns nil for a zero actor id (no resolvable employee),
// else v — company_location.created_by/deleted_by are nullable, unlike
// lkp_warehouse's NOT NULL columns, since this feature has no "system actor"
// concept to fall back to.
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

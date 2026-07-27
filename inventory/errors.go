package inventory

// errors.go — the package's error vocabulary and the PostgreSQL SQLSTATE
// predicates every store file maps against.
//
// Extracted from store.go so the item store, the unit store and the two ledger
// writers share one definition. Kept local to the package rather than imported
// from controllers/ to avoid a store -> controllers dependency.

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a uuid matches nothing live (or the row is
// soft-deleted). Controllers map it to 404 — and, for a scope denial, so that
// ids cannot be enumerated.
var ErrNotFound = errors.New("inventory record not found")

// ErrMovementAlreadyApplied is returned when a stock movement for a given
// (source document type, source line) has already been posted. It is detected
// by a unique-index violation on inventory_ledger rather than by a lookup, so
// the guarantee survives concurrent posts.
var ErrMovementAlreadyApplied = errors.New("inventory movement already applied")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring crmstore.ClientError.
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

// isUniqueViolation reports whether err is a unique-constraint violation
// (23505) — a duplicate SKU, barcode, bin code, or an already-applied movement.
func isUniqueViolation(err error) bool { return sqlState(err, "23505") }

// isCheckViolation reports whether err is a CHECK-constraint violation (23514).
// The one that matters most is chk_inventory_stock_on_hand: it is what actually
// prevents negative stock, so callers must map it to a 400 rather than a 500.
func isCheckViolation(err error) bool { return sqlState(err, "23514") }

// isFKViolation reports whether err is a foreign-key violation (23503) — a
// reference to a unit, warehouse, bin, material or reason that does not exist.
func isFKViolation(err error) bool { return sqlState(err, "23503") }

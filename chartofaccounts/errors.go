package chartofaccounts

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a uuid or slot key matches nothing live.
var ErrNotFound = errors.New("chart of accounts entry not found")

// ErrCipherUnavailable is returned when a bank account number is supplied but
// SECRET_ENCRYPTION_KEY is not configured. The store fails closed rather than
// storing the number in plaintext (AD-10), mirroring SSOOps.
var ErrCipherUnavailable = errors.New("secret encryption is not configured")

// ClientError signals a client-caused failure a controller maps to HTTP 400,
// mirroring inventory.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// ConflictError signals a state clash a controller maps to HTTP 409: an
// account referenced by a default slot, a live child blocking a delete, an
// exhausted code range, or a record-version mismatch. BlockingSlots is
// populated only for the default-slot case (AD-7).
type ConflictError struct {
	Msg           string
	BlockingSlots []string
}

func (e ConflictError) Error() string { return e.Msg }

// IsConflict reports whether err is a ConflictError, returning it for its
// BlockingSlots.
func IsConflict(err error) (ConflictError, bool) {
	var ce ConflictError
	ok := errors.As(err, &ce)
	return ce, ok
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (23505). Kept local to avoid a store->controllers import,
// mirroring inventory.isUniqueViolation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

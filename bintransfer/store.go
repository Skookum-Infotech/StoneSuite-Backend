// bintransfer/store.go — errors and helpers shared by every verb file.
package bintransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a bin transfer uuid matches no live row.
var ErrNotFound = errors.New("bin transfer not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// systemEmployeeID is the fallback actor for a soft-delete column that a CHECK
// constraint forbids leaving NULL beside its deleted_at timestamp.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID when it is unset.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

// isInvalidText reports whether err is "invalid input syntax" (22P02), which is
// what a malformed uuid in a path produces; it reads as "no such record".
func isInvalidText(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

// isForeignKeyViolation reports whether err is an FK violation (23503): a
// caller-supplied reference that does not exist.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// recordTypeIDByCode resolves a lkp_record_type code to its id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code, scoped to a record type.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// header is a bin transfer row locked or read for a write.
type header struct {
	id             int
	statusID       int
	statusCode     string
	approvalStatus string
	warehouseID    int
}

// lockHeader loads a live header by uuid and row-locks it, serialising
// concurrent writes to one document.
func lockHeader(ctx context.Context, tx pgx.Tx, uuid string) (header, error) {
	var h header
	err := tx.QueryRow(ctx, `
		SELECT bt.bin_transfer_id, bt.bin_transfer_status, rs.record_status_code,
		       bt.bin_transfer_approval_status, bt.warehouse_id
		FROM bin_transfer bt
		JOIN lkp_record_status rs ON rs.record_status_id = bt.bin_transfer_status
		WHERE bt.bin_transfer_uuid = $1 AND bt.bin_transfer_deleted_at IS NULL
		FOR UPDATE OF bt`, uuid).Scan(&h.id, &h.statusID, &h.statusCode, &h.approvalStatus, &h.warehouseID)
	if errors.Is(err, pgx.ErrNoRows) || (err != nil && isInvalidText(err)) {
		return header{}, ErrNotFound
	}
	if err != nil {
		return header{}, fmt.Errorf("lock bin transfer: %w", err)
	}
	return h, nil
}

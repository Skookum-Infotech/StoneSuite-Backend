// Package docflow holds the status-document plumbing shared by the three
// Phase 3 inventory documents (adjustment, transfer, cycle count).
//
// CLAUDE.md records that the existing relational document modules are
// copy-paste twins with no shared base, so a cross-cutting fix has to be
// hand-ported to every one of them. That is a cost worth not paying three more
// times. What is extracted here is only the part that is genuinely identical
// and carries no table names: resolving a record type and status through
// lkp_record_type / lkp_record_status, and validating a status move against a
// transition map.
//
// Deliberately NOT extracted: writing history rows and updating a document's
// status column. Those need the module's own table and column names, and
// generalising them would mean assembling SQL from identifier strings — trading
// a little duplication for a class of bug that duplication cannot cause.
package docflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Querier is the read surface docflow needs. Defined here, at the point of use,
// so callers can pass a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrUnknownStatus is returned when a status code is not seeded for the given
// record type.
var ErrUnknownStatus = errors.New("unknown status code for this record type")

// ErrUnknownRecordType is returned when a record type code is not seeded.
var ErrUnknownRecordType = errors.New("unknown record type code")

// RecordTypeIDByCode resolves a record type code ('IADJ') to its id.
//
// Always resolved by code, never hardcoded: lkp_record_status keys its rows to
// types by SERIAL assignment order, so a literal id is wrong on any tenant
// whose lookups were seeded in a different order — and wrong silently, by
// pointing an entire document class at another type's statuses.
func RecordTypeIDByCode(ctx context.Context, q Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrUnknownRecordType, code)
	}
	if err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

// StatusIDByCode resolves a status code within one record type.
func StatusIDByCode(ctx context.Context, q Querier, recordTypeID int, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrUnknownStatus, code)
	}
	if err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

// InitialStatus resolves both the record type and its opening status in one
// step, which is what every document's create path needs.
func InitialStatus(ctx context.Context, q Querier, recordTypeCode, statusCode string) (recordTypeID, statusID int, err error) {
	recordTypeID, err = RecordTypeIDByCode(ctx, q, recordTypeCode)
	if err != nil {
		return 0, 0, err
	}
	statusID, err = StatusIDByCode(ctx, q, recordTypeID, statusCode)
	if err != nil {
		return 0, 0, err
	}
	return recordTypeID, statusID, nil
}
